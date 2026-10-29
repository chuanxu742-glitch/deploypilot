package main

import "testing"

func TestContextBudgetConfiguration(t *testing.T) {
	if n, err := contextBudget("32768"); err != nil || n != 32768 {
		t.Fatalf("configured budget: %d %v", n, err)
	}
	if n, err := contextBudget(""); err != nil || n != 131072 {
		t.Fatalf("default budget: %d %v", n, err)
	}
	for _, value := range []string{"invalid", "-1", "0", "16000", "2097153"} {
		if _, err := contextBudget(value); err == nil {
			t.Errorf("invalid budget accepted: %s", value)
		}
	}
}
