package cluster

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// PartitionedClusterConfig extends ClusterConfig with Raft-specific settings.
type PartitionedClusterConfig struct {
	ClusterConfig

	// DataDir is the directory for Raft log and snapshot storage.
	DataDir string

	// RaftPort is the port for Raft consensus traffic. Defaults to BindPort + 1.
	RaftPort int

	// TotalShards is the number of shards to distribute across members.
	// Use more shards than nodes for smoother rebalancing. Defaults to 64.
	TotalShards uint64

	// Bootstrap should be true for the first node that starts the cluster.
	Bootstrap bool
}

// DefaultPartitionedClusterConfig returns sensible defaults.
func DefaultPartitionedClusterConfig() PartitionedClusterConfig {
	return PartitionedClusterConfig{
		ClusterConfig: DefaultClusterConfig(),
		DataDir:       "data",
		TotalShards:   64,
	}
}

// ApplyHandler is called when a replicated command is committed on all nodes.
type ApplyHandler func(op string, data []byte) interface{}

// PartitionedCluster embeds Cluster and adds Raft-based leader election,
// automatic shard assignment, and replicated state.
type PartitionedCluster struct {
	Cluster

	raft          *raft.Raft
	fsm           *partitionedFSM
	raftTransport *raft.NetworkTransport
	raftLogStore  *raftboltdb.BoltStore
	partConfig    PartitionedClusterConfig

	partMu         sync.RWMutex
	memberMap      map[string]uint64 // node name → member number
	shardMap       map[uint64]string // shard → node name
	totalShards    uint64
	applyHandlers  map[string]ApplyHandler
	changeHandlers []func()
}

// Start initializes the gossip layer, Raft consensus, and begins cluster formation.
func (pc *PartitionedCluster) Start(cfg PartitionedClusterConfig) error {
	pc.partConfig = cfg
	pc.memberMap = make(map[string]uint64)
	pc.shardMap = make(map[uint64]string)
	pc.applyHandlers = make(map[string]ApplyHandler)
	pc.totalShards = cfg.TotalShards
	if pc.totalShards == 0 {
		pc.totalShards = 64
	}

	// Start the gossip layer first
	if err := pc.Cluster.Start(cfg.ClusterConfig); err != nil {
		return err
	}

	// Set up Raft
	if err := pc.startRaft(cfg); err != nil {
		pc.Cluster.Stop()
		return err
	}

	// Wire gossip events to Raft membership management
	pc.Cluster.OnJoin(func(m Member) {
		pc.handleMemberJoin(m)
	})

	pc.Cluster.OnLeave(func(m Member) {
		pc.handleMemberLeave(m)
	})

	return nil
}

// Stop gracefully shuts down Raft and the gossip layer.
func (pc *PartitionedCluster) Stop() error {
	if pc.raft != nil {
		pc.raft.Shutdown().Error()
	}
	if pc.raftLogStore != nil {
		pc.raftLogStore.Close()
	}
	if pc.raftTransport != nil {
		pc.raftTransport.Close()
	}
	return pc.Cluster.Stop()
}

// Leader returns the current Raft leader.
func (pc *PartitionedCluster) Leader() Member {
	addr, _ := pc.raft.LeaderWithID()
	for _, m := range pc.Members() {
		raftAddr := net.JoinHostPort(m.Addr, strconv.Itoa(pc.raftPort()))
		if raft.ServerAddress(raftAddr) == addr {
			return m
		}
	}
	return Member{}
}

// IsLeader returns true if this node is the current Raft leader.
func (pc *PartitionedCluster) IsLeader() bool {
	return pc.raft.State() == raft.Leader
}

// MemberID returns this node's member number (0 to N-1).
func (pc *PartitionedCluster) MemberID() uint64 {
	pc.partMu.RLock()
	defer pc.partMu.RUnlock()
	return pc.memberMap[pc.Config.Name]
}

// MemberNumber returns the member number for a given node name.
func (pc *PartitionedCluster) MemberNumber(name string) (uint64, bool) {
	pc.partMu.RLock()
	defer pc.partMu.RUnlock()
	id, ok := pc.memberMap[name]
	return id, ok
}

// TotalMembers returns the current number of active members.
func (pc *PartitionedCluster) TotalMembers() uint64 {
	pc.partMu.RLock()
	defer pc.partMu.RUnlock()
	return uint64(len(pc.memberMap))
}

// GetTotalShards returns the configured number of shards.
func (pc *PartitionedCluster) GetTotalShards() uint64 {
	return pc.totalShards
}

// MyShards returns the shard IDs assigned to this node.
func (pc *PartitionedCluster) MyShards() []uint64 {
	return pc.ShardsFor(pc.Config.Name)
}

// ShardsFor returns the shard IDs assigned to the named node.
func (pc *PartitionedCluster) ShardsFor(name string) []uint64 {
	pc.partMu.RLock()
	defer pc.partMu.RUnlock()

	var shards []uint64
	for shard, owner := range pc.shardMap {
		if owner == name {
			shards = append(shards, shard)
		}
	}
	sort.Slice(shards, func(i, j int) bool { return shards[i] < shards[j] })
	return shards
}

// OwnsKey returns true if this node owns the shard for the given key.
func (pc *PartitionedCluster) OwnsKey(key string) bool {
	shard := Hash(key) % pc.totalShards
	pc.partMu.RLock()
	defer pc.partMu.RUnlock()
	return pc.shardMap[shard] == pc.Config.Name
}

// ShardForKey returns the shard ID that the given key maps to.
func (pc *PartitionedCluster) ShardForKey(key string) uint64 {
	return Hash(key) % pc.totalShards
}

// OwnerOfKey returns the node name that owns the shard for the given key.
func (pc *PartitionedCluster) OwnerOfKey(key string) string {
	shard := Hash(key) % pc.totalShards
	pc.partMu.RLock()
	defer pc.partMu.RUnlock()
	return pc.shardMap[shard]
}

// Replicate proposes a command to Raft for replication to all nodes.
func (pc *PartitionedCluster) Replicate(namespace string, op string, data []byte) (interface{}, error) {
	if pc.raft.State() != raft.Leader {
		return nil, fmt.Errorf("cluster: not the leader")
	}

	cmd := raftCommand{
		Namespace: namespace,
		Op:        op,
		Data:      data,
	}
	encoded, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("cluster: failed to marshal command: %w", err)
	}

	future := pc.raft.Apply(encoded, 5*time.Second)
	if err := future.Error(); err != nil {
		return nil, fmt.Errorf("cluster: raft apply failed: %w", err)
	}
	return future.Response(), nil
}

// OnApply registers a handler for replicated commands in the given namespace.
func (pc *PartitionedCluster) OnApply(namespace string, handler ApplyHandler) {
	pc.partMu.Lock()
	defer pc.partMu.Unlock()
	pc.applyHandlers[namespace] = handler
}

// OnChange registers a handler called whenever the shard map changes.
func (pc *PartitionedCluster) OnChange(handler func()) {
	pc.partMu.Lock()
	defer pc.partMu.Unlock()
	pc.changeHandlers = append(pc.changeHandlers, handler)
}

// Hash returns a deterministic uint64 hash for the given key.
func Hash(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return h.Sum64()
}

// --- Raft setup ---

func (pc *PartitionedCluster) raftPort() int {
	if pc.partConfig.RaftPort > 0 {
		return pc.partConfig.RaftPort
	}
	return pc.partConfig.ClusterConfig.BindPort + 1
}

func (pc *PartitionedCluster) startRaft(cfg PartitionedClusterConfig) error {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("cluster: failed to create data dir: %w", err)
	}

	raftConfig := raft.DefaultConfig()
	raftConfig.LocalID = raft.ServerID(cfg.ClusterConfig.Name)
	if cfg.ClusterConfig.LogOutput != nil {
		raftConfig.LogOutput = cfg.ClusterConfig.LogOutput
	} else {
		raftConfig.LogOutput = io.Discard
	}

	// Log store
	logStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-log.db"))
	if err != nil {
		return fmt.Errorf("cluster: failed to create log store: %w", err)
	}
	pc.raftLogStore = logStore

	// Snapshot store
	snapshotStore, err := raft.NewFileSnapshotStore(cfg.DataDir, 3, io.Discard)
	if err != nil {
		logStore.Close()
		return fmt.Errorf("cluster: failed to create snapshot store: %w", err)
	}

	// Transport
	bindAddr := cfg.ClusterConfig.BindAddr
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}
	raftAddr := fmt.Sprintf("%s:%d", bindAddr, pc.raftPort())

	advertiseAddr := cfg.ClusterConfig.AdvertiseAddr
	if advertiseAddr == "" {
		if ips := getLocalIPs(); len(ips) > 0 {
			advertiseAddr = ips[0].String()
		} else {
			advertiseAddr = bindAddr
		}
	}
	advertise, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", advertiseAddr, pc.raftPort()))
	if err != nil {
		logStore.Close()
		return fmt.Errorf("cluster: failed to resolve advertise address: %w", err)
	}

	transport, err := raft.NewTCPTransport(raftAddr, advertise, 3, 10*time.Second, io.Discard)
	if err != nil {
		logStore.Close()
		return fmt.Errorf("cluster: failed to create transport: %w", err)
	}
	pc.raftTransport = transport

	// FSM
	pc.fsm = &partitionedFSM{pc: pc}

	// Create Raft
	r, err := raft.NewRaft(raftConfig, pc.fsm, logStore, logStore, snapshotStore, transport)
	if err != nil {
		transport.Close()
		logStore.Close()
		return fmt.Errorf("cluster: failed to create raft: %w", err)
	}
	pc.raft = r

	// Bootstrap if first node
	if cfg.Bootstrap {
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      raft.ServerID(cfg.ClusterConfig.Name),
					Address: transport.LocalAddr(),
				},
			},
		}
		r.BootstrapCluster(configuration)

		go pc.waitForLeadershipAndInit()
	}

	return nil
}

func (pc *PartitionedCluster) waitForLeadershipAndInit() {
	for {
		select {
		case <-pc.stopCh:
			return
		case leader := <-pc.raft.LeaderCh():
			if leader {
				pc.rebalance()
				return
			}
		}
	}
}

// --- Raft membership management ---

func (pc *PartitionedCluster) handleMemberJoin(m Member) {
	if !pc.IsLeader() {
		return
	}

	if m.Name == pc.Config.Name {
		return
	}

	raftAddr := raft.ServerAddress(net.JoinHostPort(m.Addr, strconv.Itoa(pc.raftPort())))

	configFuture := pc.raft.GetConfiguration()
	if err := configFuture.Error(); err != nil {
		return
	}
	for _, srv := range configFuture.Configuration().Servers {
		if srv.ID == raft.ServerID(m.Name) {
			return
		}
	}

	future := pc.raft.AddVoter(raft.ServerID(m.Name), raftAddr, 0, 10*time.Second)
	if err := future.Error(); err != nil {
		pc.partLogf("Failed to add %s to raft: %v", m.Name, err)
		return
	}

	pc.partLogf("Added %s to raft cluster", m.Name)
	pc.rebalance()
}

func (pc *PartitionedCluster) handleMemberLeave(m Member) {
	if !pc.IsLeader() {
		return
	}

	if m.Name == pc.Config.Name {
		return
	}

	future := pc.raft.RemoveServer(raft.ServerID(m.Name), 0, 10*time.Second)
	if err := future.Error(); err != nil {
		pc.partLogf("Failed to remove %s from raft: %v", m.Name, err)
		return
	}

	pc.partLogf("Removed %s from raft cluster", m.Name)
	pc.rebalance()
}

func (pc *PartitionedCluster) rebalance() {
	if !pc.IsLeader() {
		return
	}

	configFuture := pc.raft.GetConfiguration()
	if err := configFuture.Error(); err != nil {
		return
	}

	servers := configFuture.Configuration().Servers
	if len(servers) == 0 {
		return
	}

	names := make([]string, 0, len(servers))
	for _, srv := range servers {
		names = append(names, string(srv.ID))
	}
	sort.Strings(names)

	memberMap := make(map[string]uint64)
	for i, name := range names {
		memberMap[name] = uint64(i)
	}

	shardMap := make(map[uint64]string)
	for shard := uint64(0); shard < pc.totalShards; shard++ {
		owner := names[shard%uint64(len(names))]
		shardMap[shard] = owner
	}

	assignment := shardAssignment{
		MemberMap:   memberMap,
		ShardMap:    shardMap,
		TotalShards: pc.totalShards,
	}
	data, _ := json.Marshal(assignment)

	cmd := raftCommand{
		Namespace: "_cluster",
		Op:        "assign",
		Data:      data,
	}
	encoded, _ := json.Marshal(cmd)

	pc.raft.Apply(encoded, 5*time.Second)
}

func (pc *PartitionedCluster) notifyChangeHandlers() {
	pc.partMu.RLock()
	handlers := pc.changeHandlers
	pc.partMu.RUnlock()

	for _, h := range handlers {
		go h()
	}
}

func (pc *PartitionedCluster) partLogf(format string, args ...interface{}) {
	if pc.partConfig.ClusterConfig.LogOutput != nil {
		fmt.Fprintf(pc.partConfig.ClusterConfig.LogOutput, "[partitioned] "+format+"\n", args...)
	}
}

// --- Raft FSM ---

type raftCommand struct {
	Namespace string          `json:"ns"`
	Op        string          `json:"op"`
	Data      json.RawMessage `json:"data"`
}

type shardAssignment struct {
	MemberMap   map[string]uint64 `json:"members"`
	ShardMap    map[uint64]string `json:"shards"`
	TotalShards uint64            `json:"total_shards"`
}

type partitionedFSM struct {
	pc *PartitionedCluster
}

func (f *partitionedFSM) Apply(log *raft.Log) interface{} {
	var cmd raftCommand
	if err := json.Unmarshal(log.Data, &cmd); err != nil {
		return err
	}

	if cmd.Namespace == "_cluster" {
		return f.applyClusterCmd(cmd)
	}

	f.pc.partMu.RLock()
	handler, ok := f.pc.applyHandlers[cmd.Namespace]
	f.pc.partMu.RUnlock()

	if ok {
		return handler(cmd.Op, cmd.Data)
	}

	return fmt.Errorf("cluster: unknown namespace %q", cmd.Namespace)
}

func (f *partitionedFSM) applyClusterCmd(cmd raftCommand) interface{} {
	switch cmd.Op {
	case "assign":
		var assignment shardAssignment
		if err := json.Unmarshal(cmd.Data, &assignment); err != nil {
			return err
		}

		f.pc.partMu.Lock()
		f.pc.memberMap = assignment.MemberMap
		f.pc.totalShards = assignment.TotalShards
		f.pc.shardMap = make(map[uint64]string)
		for shard, owner := range assignment.ShardMap {
			f.pc.shardMap[shard] = owner
		}
		f.pc.partMu.Unlock()

		f.pc.notifyChangeHandlers()
		return nil
	}
	return fmt.Errorf("cluster: unknown cluster op %q", cmd.Op)
}

func (f *partitionedFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.pc.partMu.RLock()
	defer f.pc.partMu.RUnlock()

	assignment := shardAssignment{
		MemberMap:   f.pc.memberMap,
		ShardMap:    f.pc.shardMap,
		TotalShards: f.pc.totalShards,
	}
	data, err := json.Marshal(assignment)
	if err != nil {
		return nil, err
	}
	return &partitionedSnapshot{data: data}, nil
}

func (f *partitionedFSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var assignment shardAssignment
	if err := json.NewDecoder(rc).Decode(&assignment); err != nil {
		return err
	}

	f.pc.partMu.Lock()
	f.pc.memberMap = assignment.MemberMap
	f.pc.totalShards = assignment.TotalShards
	f.pc.shardMap = make(map[uint64]string)
	for shard, owner := range assignment.ShardMap {
		f.pc.shardMap[shard] = owner
	}
	f.pc.partMu.Unlock()

	f.pc.notifyChangeHandlers()
	return nil
}

type partitionedSnapshot struct {
	data []byte
}

func (s *partitionedSnapshot) Persist(sink raft.SnapshotSink) error {
	_, err := sink.Write(s.data)
	if err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *partitionedSnapshot) Release() {}
