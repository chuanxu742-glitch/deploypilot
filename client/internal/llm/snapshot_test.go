package llm

import (
	"fmt"
	"sync"
	"testing"
)

func TestSnapshotRetainsTaskConfiguration(t *testing.T) {
	client := New(Config{APIKey: "original-key", Model: "original-model"})
	snapshot := client.Snapshot()
	client.SetAPIKey("new-key")
	client.SetModel("new-model")
	if snapshot.Model() != "original-model" || snapshot.Redact("original-key") != "***" {
		t.Fatal("running task configuration changed")
	}
	if client.Model() != "new-model" || client.Redact("new-key") != "***" {
		t.Fatal("settings update lost")
	}
}

func TestConcurrentSettingsAndSnapshots(t *testing.T) {
	client := New(Config{APIKey: "key", Model: "model"})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				client.SetAPIKey(fmt.Sprintf("key-%d", id))
				client.SetModel("model")
				snapshot := client.Snapshot()
				if !snapshot.HasKey() || snapshot.Model() != "model" {
					t.Error("incomplete snapshot")
				}
				client.Redact("key")
				client.HasKey()
				client.Model()
			}
		}(i)
	}
	wg.Wait()
}
