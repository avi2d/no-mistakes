package types

import "testing"

func TestParseFindingsJSON_RoundTripsTheCarriedTag(t *testing.T) {
	t.Parallel()
	encoded, err := MarshalFindingsJSON(Findings{Items: []Finding{
		{ID: "review-1", Severity: FindingSeverityWarning, Description: "fixed earlier", Carried: FindingCarriedAwaitingVerification},
		{ID: "review-2", Severity: FindingSeverityWarning, Description: "left for later", Carried: FindingCarriedUnselected},
		{ID: "review-3", Severity: FindingSeverityWarning, Description: "reported this round"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseFindingsJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := []FindingCarry{FindingCarriedAwaitingVerification, FindingCarriedUnselected, ""}
	for i, carry := range want {
		if parsed.Items[i].Carried != carry {
			t.Errorf("item %s carried = %q, want %q", parsed.Items[i].ID, parsed.Items[i].Carried, carry)
		}
	}
}

func TestParseFindingsJSON_DropsAnUnknownCarriedTag(t *testing.T) {
	t.Parallel()
	parsed, err := ParseFindingsJSON(`{"findings":[{"id":"review-1","severity":"warning","description":"x","carried":"verified"}],"summary":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Items[0].Carried != "" {
		t.Fatalf("an unknown carried value %q was kept", parsed.Items[0].Carried)
	}
}
