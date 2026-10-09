// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package kafkaclient checks the Kafka clients the server and projector build
// against docs/specs/kafka/client-contract.md (AW-SRV-053).
//
// Each package that builds a client describes it as a Site: the options it
// builds the client with, and what the client is for. Check builds the client
// from those options, without connecting to anything a caller names, and reads
// the settings back with kgo's OptValue, so the answer is what the library
// holds and not what a comment says. Until the single client constructor of
// AW-SRV-044 lands, a drift guard test in this package counts the kgo.NewClient
// calls in the tree and fails when one has no Site.
package kafkaclient

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Role is what a client does on the log.
type Role int

const (
	// Producer appends records.
	Producer Role = iota
	// Consumer reads records.
	Consumer
	// Admin asks metadata and offsets, and neither produces nor consumes.
	Admin
)

// Partitioning is how a producer picks a Partition.
type Partitioning int

const (
	// Manual: the caller sets Record.Partition, from sim.PartitionFor on the
	// 64-Partition ZoneID-keyed topics.
	Manual Partitioning = iota + 1
	// KeyHash: the library's murmur2 hash of the record key, pinned
	// explicitly on the 6-Partition keyed topics.
	KeyHash
)

// OneShot names a reader in the contract's table of readers that start at
// AtStart or AtEnd because they read for content and resume no position.
type OneShot string

const (
	NotOneShot           OneShot = ""
	AccountsScan         OneShot = "accounts"
	ContentBlobsScan     OneShot = "content blobs"
	ContentVersionsScan  OneShot = "content versions"
	ContentActiveWatch   OneShot = "content active pointer (watch)"
	AuditReplay          OneShot = "audit replay"
	TickBoundaryScan     OneShot = "tick boundary scan"
	contractReaderTable          = "client-contract.md, Consumers"
	contractClientIDRule         = "^<principal>(-[a-z]+)*$"
)

var oneShots = map[OneShot]bool{
	AccountsScan: true, ContentBlobsScan: true, ContentVersionsScan: true,
	ContentActiveWatch: true, AuditReplay: true, TickBoundaryScan: true,
}

// Principals are the Kafka principals of ADR-0011 §3; a client.id starts with
// one of them.
var Principals = []string{
	"andara-server", "andara-projector-state", "andara-projection-redis", "andara-projection-pg", "andara-operator",
}

var clientID = regexp.MustCompile(`^(` + strings.Join(Principals, "|") + `)(-[a-z]+)*$`)

// Site is one place the code builds a Kafka client.
type Site struct {
	// Name says where, for a person: "ingress producer".
	Name string
	Role Role
	// Opts are the options the code builds the client with. They name the
	// seed brokers the caller passed in; a check passes an address nothing
	// listens on.
	Opts []kgo.Opt

	// Producers.
	Partitioning    Partitioning
	DeliveryTimeout time.Duration // the deadline the client must hold; 0 asserts nothing

	// Consumers: NotOneShot for a reader that resumes a position.
	OneShot OneShot
	// AssignedLater: the code gives the client its Partitions with
	// AddConsumePartitions after building it, at At(offset). The check cannot
	// see those offsets and takes the start offset row as met.
	AssignedLater bool
}

// Severity of a finding.
type Severity int

const (
	// Warn is a deviation that carries no correctness claim.
	Warn Severity = iota + 1
	// Fail is a deviation from a setting a claim depends on.
	Fail
)

func (s Severity) String() string {
	if s == Warn {
		return "warn"
	}
	return "fail"
}

// Finding is one row of the report: a setting, what is in effect, and what
// the contract says.
type Finding struct {
	Client   string
	Setting  string
	Value    string
	Contract string
	Severity Severity
	OK       bool
}

func (f Finding) String() string {
	return fmt.Sprintf("client=%q setting=%s value=%s contract=%s", f.Client, f.Setting, f.Value, f.Contract)
}

// Check builds the Site's client and reads back its settings, one Finding per
// row of the contract that applies to its Role. It dials nothing: the client
// is closed before it is used.
func Check(s Site) []Finding {
	cl, err := kgo.NewClient(s.Opts...)
	if err != nil {
		return []Finding{{Client: s.Name, Setting: "client", Value: err.Error(), Contract: "builds", Severity: Fail}}
	}
	defer cl.Close()

	var out []Finding
	add := func(setting, value, contract string, sev Severity, ok bool) {
		out = append(out, Finding{Client: s.Name, Setting: setting, Value: value, Contract: contract, Severity: sev, OK: ok})
	}

	id, _ := cl.OptValue(kgo.ClientID).(string)
	add("client.id", fmt.Sprintf("%q", id), contractClientIDRule, Fail, clientID.MatchString(id))

	switch s.Role {
	case Producer:
		checkProducer(cl, s, add)
	case Consumer:
		checkConsumer(cl, s, add)
	}
	return out
}

type adder func(setting, value, contract string, sev Severity, ok bool)

func checkProducer(cl *kgo.Client, s Site, add adder) {
	acks, _ := cl.OptValue(kgo.RequiredAcks).(kgo.Acks)
	add("acks", fmt.Sprint(acks), "all", Fail, acks == kgo.AllISRAcks())

	idempotenceOff, _ := cl.OptValue(kgo.DisableIdempotentWrite).(bool)
	add("enable.idempotence", fmt.Sprint(!idempotenceOff), "true", Fail, !idempotenceOff)

	// franz-go pins max.in.flight for an idempotent producer; it is
	// reported at its pinned value, and anything above 5 fails.
	inflight, _ := cl.OptValue(kgo.MaxProduceRequestsInflightPerBroker).(int)
	add("max.in.flight.requests.per.connection", fmt.Sprint(inflight), "<= 5", Fail, inflight <= 5)

	if s.DeliveryTimeout > 0 {
		got, _ := cl.OptValue(kgo.RecordDeliveryTimeout).(time.Duration)
		add("delivery.timeout.ms", got.String(), s.DeliveryTimeout.String()+" (ingress.produce_deadline)", Fail, got == s.DeliveryTimeout)
	}

	switch s.Partitioning {
	case Manual:
		ok, got := manualPartitioner(cl)
		add("partitioner", got, "explicit hash(ZoneID) % 64, set by the caller", Fail, ok)
	case KeyHash:
		ok, got := keyHashPartitioner(cl)
		add("partitioner", got, "murmur2 of the record key, pinned", Fail, ok)
	}

	codecs, _ := cl.OptValue(kgo.ProducerBatchCompression).([]kgo.CompressionCodec)
	zstd := len(codecs) > 0 && codecs[0] == kgo.ZstdCompression()
	add("compression.type", compressionName(codecs), "zstd", Warn, zstd)
}

func compressionName(codecs []kgo.CompressionCodec) string {
	if len(codecs) == 0 {
		return "none"
	}
	switch codecs[0] {
	case kgo.ZstdCompression():
		return "zstd"
	case kgo.NoCompression():
		return "none"
	case kgo.SnappyCompression():
		return "snappy"
	case kgo.Lz4Compression():
		return "lz4"
	case kgo.GzipCompression():
		return "gzip"
	}
	return "other"
}

func checkConsumer(cl *kgo.Client, s Site, add adder) {
	// The library stores the level as its wire value: 0 read_uncommitted, 1
	// read_committed.
	level, _ := cl.OptValue(kgo.FetchIsolationLevel).(int8)
	add("isolation.level", isolationName(level), "read_committed", Fail, level == readCommitted)

	group, _ := cl.OptValue(kgo.ConsumerGroup).(string)
	add("group membership", groupName(group), "none; direct Partition assignment", Fail, group == "")

	// A group is the only way franz-go commits offsets to Kafka; a client
	// without one cannot, so the row is the group row. Marking commits
	// without a group is meaningless and is reported anyway.
	marks, _ := cl.OptValue(kgo.AutoCommitMarks).(bool)
	add("committed offsets in Kafka", fmt.Sprint(group != "" || marks), "none", Fail, group == "" && !marks)

	checkStart(cl, s, add)
}

const readCommitted int8 = 1

func isolationName(l int8) string {
	switch l {
	case readCommitted:
		return "read_committed"
	case 0:
		return "read_uncommitted"
	}
	return fmt.Sprintf("level %d", l)
}

func groupName(g string) string {
	if g == "" {
		return "none"
	}
	return fmt.Sprintf("%q", g)
}

// checkStart is the start offset row. A reader with explicit Partition
// assignments resumes at At(offset). A reader that consumes topics starts
// where ConsumeResetOffset says, which is the start or the end of the log. A
// reader that is not At(offset) must be a named one-shot of the contract.
func checkStart(cl *kgo.Client, s Site, add adder) {
	var starts []string
	explicit := true
	if parts, _ := cl.OptValue(kgo.ConsumePartitions).(map[string]map[int32]kgo.Offset); len(parts) > 0 {
		for _, ps := range parts {
			for _, o := range ps {
				if at := o.EpochOffset().Offset; at < 0 {
					explicit = false
					starts = append(starts, offsetName(at))
				}
			}
		}
	} else if !s.AssignedLater {
		explicit = false
		reset, _ := cl.OptValue(kgo.ConsumeResetOffset).(kgo.Offset)
		starts = append(starts, offsetName(reset.EpochOffset().Offset))
	}
	value := "At(offset)"
	if !explicit {
		value = strings.Join(starts, ", ")
	}
	switch {
	case explicit && s.OneShot == NotOneShot:
		add("start offset", value, "At(offset) from the checkpoint", Fail, true)
	case explicit:
		add("start offset", value, "a named one-shot starts at AtStart or AtEnd", Fail, false)
	case oneShots[s.OneShot]:
		add("start offset", value+` (one-shot "`+string(s.OneShot)+`")`, "AtStart/AtEnd only for the named one-shot readers ("+contractReaderTable+")", Fail, true)
	default:
		add("start offset", value, "At(offset); AtStart/AtEnd only for the named one-shot readers ("+contractReaderTable+")", Fail, false)
	}
}

func offsetName(at int64) string {
	switch at {
	case -2:
		return "AtStart"
	case -1:
		return "AtEnd"
	}
	return fmt.Sprintf("At(%d)", at)
}

// manualPartitioner is true when the client's partitioner returns the
// Partition the record already carries, for every Partition of a 64-Partition
// topic: the caller's sim.PartitionFor is the only hash.
func manualPartitioner(cl *kgo.Client) (bool, string) {
	p, _ := cl.OptValue(kgo.RecordPartitioner).(kgo.Partitioner)
	if p == nil {
		return false, "default"
	}
	tp := p.ForTopic("t")
	// Descending, so a partitioner that counts up cannot pass by coincidence.
	for want := int32(63); want >= 0; want-- {
		r := &kgo.Record{Key: []byte("zone"), Partition: want}
		if got := partitionOf(tp, r, 64); got != int(want) {
			return false, fmt.Sprintf("hashes the key: Partition %d became %d", want, got)
		}
	}
	return true, "manual"
}

// keyHashSample is the Partition on 6 Partitions that keys have today under
// the library's default partitioner, which the topics' history was written
// under. A library upgrade or a different pinned partitioner that moves one
// fails the check, instead of splitting a key's compaction history.
var keyHashSample = map[string]int{
	"operator":            5,
	"acct-0001":           5,
	"acct-0002":           4,
	"player-ada":          1,
	"player-bram":         4,
	"character:7f3a":      2,
	"content/andara.core": 3,
	"andara.core@v1":      1,
	"zone-harbor":         1,
	"a":                   4,
	"bb":                  4,
	"ccc":                 0,
	"0123456789abcdef":    0,
}

// keyHashPartitioner is true when the client maps keyed records as Kafka's
// default partitioner does (murmur2 of the key, positive, modulo the
// Partition count), the mapping the topics' history was written under.
func keyHashPartitioner(cl *kgo.Client) (bool, string) {
	p, _ := cl.OptValue(kgo.RecordPartitioner).(kgo.Partitioner)
	if p == nil {
		return false, "none"
	}
	tp := p.ForTopic("t")
	for k, want := range keyHashSample {
		if got := partitionOf(tp, &kgo.Record{Key: []byte(k)}, 6); got != want {
			return false, fmt.Sprintf("key %q maps to Partition %d, history has %d", k, got, want)
		}
	}
	return true, "murmur2 of the key"
}

// partitionOf asks a topic partitioner where a keyed record goes. A
// partitioner that wants the buffer's state (the library's default does, for
// unkeyed records) is given an empty one: a keyed record never reads it.
func partitionOf(tp kgo.TopicPartitioner, r *kgo.Record, n int) int {
	if b, ok := tp.(kgo.TopicBackupPartitioner); ok {
		return b.PartitionByBackup(r, n, emptyBackup{})
	}
	return tp.Partition(r, n)
}

type emptyBackup struct{}

func (emptyBackup) Next() (int, int64) { panic("kafkaclient: a keyed record read the buffer") }
func (emptyBackup) Rem() int           { return 0 }
