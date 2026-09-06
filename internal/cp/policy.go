package cp

// Placement policy: given the hosts that could take a sandbox, which one does. The
// scheduler builds the candidates — approved, online, room, the right tags — and the policy
// only ranks them, so a new rule (least loaded, sticky per owner, resource-aware) is one
// type here and a different argument to NewScheduler, never a change to the loop.

// Candidate is an approved, online host with room and the right tags.
type Candidate struct {
	HostID string
	Free   int
}

// Policy chooses among candidates, or returns "" to queue.
type Policy interface {
	Pick(candidates []Candidate) string
}

// MostFreeSlots is DESIGN.md decision 8: the host with the most free slots wins.
var MostFreeSlots Policy = mostFreeSlots{}

type mostFreeSlots struct{}

func (mostFreeSlots) Pick(candidates []Candidate) string {
	best := ""
	free := 0
	for _, c := range candidates {
		if best == "" || c.Free > free {
			best, free = c.HostID, c.Free
		}
	}
	return best
}
