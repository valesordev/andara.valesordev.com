// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/kafkaclient"
)

func realSites() []kafkaclient.Site {
	return KafkaSites(config.Config{ServiceName: "andara-server", IngressProduceDeadline: 5 * time.Second})
}

// The clients the code builds hold the contract: exit 0, every row printed.
func TestConfigAssert_TheClientsTheCodeBuildsHoldTheContract(t *testing.T) {
	var out, errs bytes.Buffer
	if code := ConfigAssert(realSites(), &out, &errs); code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errs.String())
	}
	if strings.Contains(out.String(), "FAIL") {
		t.Fatalf("a failing row printed:\n%s", out.String())
	}
	for _, want := range []string{"ingress producer", "tick boundary publisher", "projector state producer", "accounts producer", "audit replay", "content active pointer watch", "andara-server-recovery command source", "andara-projector-state boundary consumer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no row for %q", want)
		}
	}
}

// The story sets zstd on every producer. The compression row warns by design,
// so "no FAIL" would let a producer lose it: this holds each real producer to it.
func TestConfigAssert_EveryRealProducerCompressesWithZstd(t *testing.T) {
	producers := 0
	for _, s := range realSites() {
		if s.Role != kafkaclient.Producer {
			continue
		}
		producers++
		for _, f := range kafkaclient.Check(s) {
			if f.Setting == "compression.type" && !f.OK {
				t.Errorf("%s: %s", s.Name, f)
			}
		}
	}
	if producers < 8 {
		t.Fatalf("%d producers described", producers)
	}
}

// Every real reader that resumes a position says so to the library: an offset
// the broker no longer has is an error, not a silent reset.
func TestConfigAssert_EveryResumingReaderRefusesASilentReset(t *testing.T) {
	resumers := 0
	for _, s := range realSites() {
		for _, f := range kafkaclient.Check(s) {
			if f.Setting == "offset the broker no longer has" {
				resumers++
				if !f.OK {
					t.Errorf("%s: %s", s.Name, f)
				}
			}
		}
	}
	if resumers < 6 {
		t.Fatalf("%d resuming readers checked", resumers)
	}
}

// AC-1: a deviation exits 1 and names the client, the setting, the value in
// effect and the contract's value on stderr.
func TestConfigAssert_ADeviationExitsOneAndNamesIt(t *testing.T) {
	sites := realSites()
	sites = append(sites, kafkaclient.Site{Name: "drifted producer", Role: kafkaclient.Producer, Partitioning: kafkaclient.Manual,
		Opts: []kgo.Opt{kgo.SeedBrokers("127.0.0.1:1"), kgo.ClientID("andara-server-x"), kgo.RecordPartitioner(kgo.ManualPartitioner()),
			kgo.ProducerBatchCompression(kgo.ZstdCompression()), kgo.DisableIdempotentWrite()}})
	var out, errs bytes.Buffer
	if code := ConfigAssert(sites, &out, &errs); code != ExitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, want := range []string{`client="drifted producer"`, "setting=enable.idempotence", "value=false", "contract=true"} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs.String())
		}
	}
}

// AC-4: compression alone warns and exits 0.
func TestConfigAssert_CompressionWarnsAndExitsZero(t *testing.T) {
	site := kafkaclient.Site{Name: "plain producer", Role: kafkaclient.Producer, Partitioning: kafkaclient.Manual,
		Opts: []kgo.Opt{kgo.SeedBrokers("127.0.0.1:1"), kgo.ClientID("andara-server-x"), kgo.RecordPartitioner(kgo.ManualPartitioner())}}
	var out, errs bytes.Buffer
	if code := ConfigAssert([]kafkaclient.Site{site}, &out, &errs); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, errs.String())
	}
	if !strings.Contains(out.String(), "warn  ") || !strings.Contains(errs.String(), "level=WARN") {
		t.Fatalf("no warning:\n%s\n%s", out.String(), errs.String())
	}
}

var newClient = regexp.MustCompile(`kgo\.NewClient\(([^)]*)\)`)

// Until AW-SRV-044's single constructor, every kgo.NewClient call in the tree
// builds from an options function or list that a Site also describes. A call
// with its options written inline (kgo.SeedBrokers(...) in the call) has no
// Site, so the check could not see it.
func TestConfigAssert_EveryKafkaClientTheCodeBuildsHasASite(t *testing.T) {
	root := filepath.Join("..", "..")
	want := map[string]int{
		"server/recordlog/kafka.go":    2,
		"server/projector/kafka.go":    3,
		"cmd/andara-projector/main.go": 1,
		"server/content/kafka.go":      4,
		"server/ingress/producer.go":   2,
		"server/tickloop/kafka.go":     5,
		"server/tickloop/reader.go":    4,
	}
	got := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "gen" || d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "server/kafkaclient/") {
			return nil // builds the Sites' clients, to read them back
		}
		for _, m := range newClient.FindAllStringSubmatch(string(src), -1) {
			got[rel]++
			if arg := m[1]; strings.Contains(arg, "kgo.") {
				t.Errorf("%s: kgo.NewClient(%s) takes its options inline, so config-assert cannot see them", rel, arg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f, n := range want {
		if got[f] != n {
			t.Errorf("%s builds %d Kafka clients, the guard expects %d: add or remove its Site, then this count", f, got[f], n)
		}
	}
	for f := range got {
		if _, ok := want[f]; !ok {
			t.Errorf("%s builds a Kafka client and has no Site", f)
		}
	}
}
