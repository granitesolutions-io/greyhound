package cluster

import (
	"encoding/json"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/hashicorp/memberlist"
)

const (
	defaultGossipPort = 7946
)

// Member represents a node in the cluster.
type Member struct {
	Name string
	Addr string
	Port int
}

// ClusterConfig configures cluster formation and peer discovery.
type ClusterConfig struct {
	// Name is a unique name for this node. Defaults to hostname.
	Name string

	// BindAddr is the address to bind the gossip listener. Defaults to "0.0.0.0".
	BindAddr string

	// BindPort is the port for gossip communication. Defaults to 7946.
	BindPort int

	// AdvertiseAddr is the address advertised to other nodes.
	AdvertiseAddr string

	// AdvertisePort is the port advertised to other nodes. Defaults to BindPort.
	AdvertisePort int

	// Peers is an optional list of known peer addresses (host:port).
	Peers []string

	// EnableMDNS enables mDNS for automatic peer discovery on local networks.
	EnableMDNS bool

	// MDNSServiceName is the mDNS service name to advertise/query.
	// Defaults to "_cluster._tcp".
	MDNSServiceName string

	// LogOutput receives internal log output. Defaults to discard.
	LogOutput io.Writer
}

// DefaultClusterConfig returns sensible defaults with mDNS enabled.
func DefaultClusterConfig() ClusterConfig {
	hostname, _ := os.Hostname()
	return ClusterConfig{
		Name:            hostname,
		BindAddr:        "0.0.0.0",
		BindPort:        defaultGossipPort,
		EnableMDNS:      true,
		MDNSServiceName: "_cluster._tcp",
	}
}

// clusterMessage is the wire format for all gossip messages.
type clusterMessage struct {
	From  string `json:"from"`
	Topic string `json:"topic"`
	Data  []byte `json:"data"`
}

// MessageHandler is called when a message arrives on a subscribed topic.
type MessageHandler func(from Member, data []byte)

// MemberHandler is called when a member joins or leaves the cluster.
type MemberHandler func(Member)

// Cluster provides gossip-based peer discovery and messaging.
// Embed this in your struct to get cluster membership, broadcasting,
// and point-to-point messaging.
type Cluster struct {
	Config     ClusterConfig
	list       *memberlist.Memberlist
	broadcasts *memberlist.TransmitLimitedQueue
	mdnsServer *mdns.Server
	stopCh     chan struct{}

	mu              sync.RWMutex
	messageHandlers map[string][]MessageHandler
	joinHandlers    []MemberHandler
	leaveHandlers   []MemberHandler
}

// Start initializes the gossip layer and begins peer discovery.
func (c *Cluster) Start(cfg ClusterConfig) error {
	c.Config = cfg
	c.stopCh = make(chan struct{})
	c.messageHandlers = make(map[string][]MessageHandler)

	if c.Config.MDNSServiceName == "" {
		c.Config.MDNSServiceName = "_cluster._tcp"
	}

	mlConfig := memberlist.DefaultLANConfig()
	mlConfig.Name = cfg.Name
	mlConfig.BindAddr = cfg.BindAddr
	mlConfig.BindPort = cfg.BindPort
	if cfg.AdvertiseAddr != "" {
		mlConfig.AdvertiseAddr = cfg.AdvertiseAddr
	}
	if cfg.AdvertisePort > 0 {
		mlConfig.AdvertisePort = cfg.AdvertisePort
	} else {
		mlConfig.AdvertisePort = cfg.BindPort
	}

	mlConfig.LogOutput = &logWriter{output: cfg.LogOutput}

	mlConfig.Delegate = &clusterDelegate{c: c}
	mlConfig.Events = &clusterEvents{c: c}

	list, err := memberlist.Create(mlConfig)
	if err != nil {
		return fmt.Errorf("cluster: failed to create memberlist: %w", err)
	}
	c.list = list

	c.broadcasts = &memberlist.TransmitLimitedQueue{
		NumNodes:       func() int { return list.NumMembers() },
		RetransmitMult: 3,
	}

	// Discover peers
	if len(cfg.Peers) == 0 {
		if serviceName := detectServiceName(); serviceName != "" {
			serviceURL := fmt.Sprintf("http://%s:8080/api/cluster/peers", serviceName)
			go c.httpDiscoveryLoop(serviceURL)
		} else if cfg.EnableMDNS {
			c.startMDNS()
		}
	} else {
		joined, err := list.Join(cfg.Peers)
		if err != nil {
			c.logf("Peer join attempt: %v", err)
		} else if joined > 0 {
			c.logf("Joined %d cluster peer(s)", joined)
		}
		go c.rejoinLoop(cfg.Peers)
	}

	return nil
}

// Stop gracefully leaves the cluster and shuts down.
func (c *Cluster) Stop() error {
	close(c.stopCh)

	if c.mdnsServer != nil {
		c.mdnsServer.Shutdown()
	}

	if c.list != nil {
		return c.list.Leave(5 * time.Second)
	}
	return nil
}

// LocalMember returns this node's identity.
func (c *Cluster) LocalMember() Member {
	if c.list == nil {
		return Member{Name: c.Config.Name}
	}
	node := c.list.LocalNode()
	return Member{
		Name: node.Name,
		Addr: node.Addr.String(),
		Port: int(node.Port),
	}
}

// Members returns all known cluster members.
func (c *Cluster) Members() []Member {
	if c.list == nil {
		return nil
	}
	nodes := c.list.Members()
	members := make([]Member, 0, len(nodes))
	for _, n := range nodes {
		members = append(members, Member{
			Name: n.Name,
			Addr: n.Addr.String(),
			Port: int(n.Port),
		})
	}
	return members
}

// NumMembers returns the number of known cluster members.
func (c *Cluster) NumMembers() int {
	if c.list == nil {
		return 0
	}
	return c.list.NumMembers()
}

// Broadcast sends a message to all cluster members via gossip.
func (c *Cluster) Broadcast(topic string, data []byte) error {
	msg := clusterMessage{
		From:  c.list.LocalNode().Name,
		Topic: topic,
		Data:  data,
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("cluster: failed to marshal message: %w", err)
	}
	c.broadcasts.QueueBroadcast(&clusterBroadcast{data: encoded})
	return nil
}

// Send sends a message directly to a specific member.
func (c *Cluster) Send(memberName string, topic string, data []byte) error {
	var target *memberlist.Node
	for _, n := range c.list.Members() {
		if n.Name == memberName {
			target = n
			break
		}
	}
	if target == nil {
		return fmt.Errorf("cluster: member %q not found", memberName)
	}

	msg := clusterMessage{
		From:  c.list.LocalNode().Name,
		Topic: topic,
		Data:  data,
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("cluster: failed to marshal message: %w", err)
	}
	return c.list.SendReliable(target, encoded)
}

// SendAny sends a message to a random member (not self).
func (c *Cluster) SendAny(topic string, data []byte) error {
	local := c.list.LocalNode().Name
	for _, n := range c.list.Members() {
		if n.Name != local {
			return c.Send(n.Name, topic, data)
		}
	}
	return fmt.Errorf("cluster: no other members available")
}

// OnMessage registers a handler for messages on the given topic.
func (c *Cluster) OnMessage(topic string, handler MessageHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageHandlers[topic] = append(c.messageHandlers[topic], handler)
}

// OnJoin registers a handler called when a member joins the cluster.
func (c *Cluster) OnJoin(handler MemberHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.joinHandlers = append(c.joinHandlers, handler)
}

// OnLeave registers a handler called when a member leaves the cluster.
func (c *Cluster) OnLeave(handler MemberHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.leaveHandlers = append(c.leaveHandlers, handler)
}

// ClusterPeersHandler returns an HTTP handler for peer discovery.
// Mount this at /api/cluster/peers for Kubernetes-based discovery.
func (c *Cluster) ClusterPeersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		localIP := ""
		if ips := getLocalIPs(); len(ips) > 0 {
			localIP = ips[0].String()
		}

		gossipAddr := fmt.Sprintf("%s:%d", localIP, c.Config.BindPort)

		var peers []string
		if c.list != nil {
			for _, m := range c.list.Members() {
				addr := fmt.Sprintf("%s:%d", m.Addr.String(), m.Port)
				if addr != gossipAddr {
					peers = append(peers, addr)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"gossip_addr": gossipAddr,
			"peers":       peers,
			"members":     c.list.NumMembers(),
		})
	}
}

// --- internals ---

func (c *Cluster) handleMessage(data []byte) {
	var msg clusterMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	if c.list != nil && msg.From == c.list.LocalNode().Name {
		return
	}

	from := Member{Name: msg.From}
	for _, n := range c.list.Members() {
		if n.Name == msg.From {
			from.Addr = n.Addr.String()
			from.Port = int(n.Port)
			break
		}
	}

	c.mu.RLock()
	handlers := c.messageHandlers[msg.Topic]
	c.mu.RUnlock()

	for _, h := range handlers {
		h(from, msg.Data)
	}
}

func (c *Cluster) notifyJoin(node *memberlist.Node) {
	m := Member{Name: node.Name, Addr: node.Addr.String(), Port: int(node.Port)}

	c.mu.RLock()
	handlers := c.joinHandlers
	c.mu.RUnlock()

	for _, h := range handlers {
		h(m)
	}
}

func (c *Cluster) notifyLeave(node *memberlist.Node) {
	m := Member{Name: node.Name, Addr: node.Addr.String(), Port: int(node.Port)}

	c.mu.RLock()
	handlers := c.leaveHandlers
	c.mu.RUnlock()

	for _, h := range handlers {
		h(m)
	}
}

func (c *Cluster) rejoinLoop(peers []string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			joined, _ := c.list.Join(peers)
			if joined > 0 {
				c.logf("Cluster healed: joined %d new peer(s)", joined)
			}
		case <-c.stopCh:
			return
		}
	}
}

func (c *Cluster) httpDiscoveryLoop(serviceURL string) {
	time.Sleep(3 * time.Second)

	for i := 0; i < 5; i++ {
		if c.list.NumMembers() > 1 {
			return
		}
		c.discoverViaHTTP(serviceURL)
		select {
		case <-time.After(3 * time.Second):
		case <-c.stopCh:
			return
		}
	}
}

func (c *Cluster) discoverViaHTTP(serviceURL string) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(serviceURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var result struct {
		GossipAddr string   `json:"gossip_addr"`
		Peers      []string `json:"peers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}

	if result.GossipAddr != "" {
		c.list.Join([]string{result.GossipAddr})
	}
	for _, peer := range result.Peers {
		c.list.Join([]string{peer})
	}
}

func (c *Cluster) startMDNS() {
	port := c.Config.BindPort
	host, _ := os.Hostname()
	ips := getLocalIPs()

	service, err := mdns.NewMDNSService(host, c.Config.MDNSServiceName, "", "", port, ips, []string{"cluster node"})
	if err != nil {
		c.logf("mDNS service creation failed: %v", err)
		return
	}

	origOutput := stdlog.Writer()
	stdlog.SetOutput(io.Discard)
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	stdlog.SetOutput(origOutput)
	if err != nil {
		c.logf("mDNS server start failed: %v", err)
		return
	}
	c.mdnsServer = server

	go c.discoverPeers()
}

func (c *Cluster) discoverPeers() {
	time.Sleep(2 * time.Second)
	c.runDiscovery()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.runDiscovery()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Cluster) runDiscovery() {
	entriesCh := make(chan *mdns.ServiceEntry, 10)

	go func() {
		for entry := range entriesCh {
			addr := net.JoinHostPort(entry.AddrV4.String(), strconv.Itoa(entry.Port))
			if c.isSelf(entry.AddrV4, entry.Port) {
				continue
			}
			joined, err := c.list.Join([]string{addr})
			if err != nil {
				c.logf("Peer join failed (%s): %v", addr, err)
			} else if joined > 0 {
				c.logf("Found peer: %s", addr)
			}
		}
	}()

	params := mdns.DefaultParams(c.Config.MDNSServiceName)
	params.Entries = entriesCh
	params.Timeout = 3 * time.Second
	params.DisableIPv6 = true

	origOutput := stdlog.Writer()
	stdlog.SetOutput(io.Discard)
	mdns.Query(params)
	stdlog.SetOutput(origOutput)
	close(entriesCh)
}

func (c *Cluster) isSelf(addr net.IP, port int) bool {
	if c.list == nil {
		return false
	}
	self := c.list.LocalNode()
	return self.Addr.Equal(addr) && int(self.Port) == port
}

func (c *Cluster) logf(format string, args ...interface{}) {
	if c.Config.LogOutput != nil {
		fmt.Fprintf(c.Config.LogOutput, "[cluster] "+format+"\n", args...)
	}
}

// --- memberlist delegates ---

type clusterDelegate struct {
	c *Cluster
}

func (d *clusterDelegate) NodeMeta(limit int) []byte              { return nil }
func (d *clusterDelegate) NotifyMsg(msg []byte)                   { d.c.handleMessage(msg) }
func (d *clusterDelegate) LocalState(join bool) []byte            { return nil }
func (d *clusterDelegate) MergeRemoteState(buf []byte, join bool) {}
func (d *clusterDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	return d.c.broadcasts.GetBroadcasts(overhead, limit)
}

type clusterEvents struct {
	c *Cluster
}

func (e *clusterEvents) NotifyJoin(node *memberlist.Node)   { e.c.notifyJoin(node) }
func (e *clusterEvents) NotifyLeave(node *memberlist.Node)  { e.c.notifyLeave(node) }
func (e *clusterEvents) NotifyUpdate(node *memberlist.Node) {}

type clusterBroadcast struct {
	data []byte
}

func (b *clusterBroadcast) Invalidates(other memberlist.Broadcast) bool { return false }
func (b *clusterBroadcast) Message() []byte                             { return b.data }
func (b *clusterBroadcast) Finished()                                   {}

// --- utilities ---

func detectServiceName() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return ""
	}

	parts := strings.Split(hostname, "-")
	if len(parts) < 3 {
		return ""
	}

	serviceName := strings.Join(parts[:len(parts)-2], "-")

	addrs, err := net.LookupHost(serviceName)
	if err != nil || len(addrs) == 0 {
		return ""
	}

	return serviceName
}

func getLocalIPs() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.To4() != nil {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

type logWriter struct {
	output io.Writer
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	if lw.output != nil {
		return lw.output.Write(p)
	}
	return len(p), nil
}
