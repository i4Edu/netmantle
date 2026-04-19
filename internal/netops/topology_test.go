package netops

import "testing"

func TestParseAndMerge(t *testing.T) {
	out1 := `
Local Intf      Chassis ID         Port ID         System Name
Et1             001c.7300.1234     Et2             switch-2
Et3             001c.7300.5678     Gi0/1           switch-3
`
	out2 := `
Local Intf      Chassis ID         Port ID         System Name
Et2             001c.7300.0001     Et1             switch-1
`
	links := map[string][]Link{
		"switch-1": FromNeighborOutput("switch-1", out1),
		"switch-2": FromNeighborOutput("switch-2", out2),
	}
	if len(links["switch-1"]) != 2 {
		t.Fatalf("switch-1 links: %+v", links["switch-1"])
	}
	g := Merge(links)
	// switch-1↔switch-2 reciprocates: deduped to one link. switch-1↔switch-3
	// remains: total 2.
	if len(g.Links) != 2 {
		t.Fatalf("merged: %+v", g.Links)
	}
}
