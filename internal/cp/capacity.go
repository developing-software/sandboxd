package cp

// Capacity is what a host reports about itself, live: it is never read from a row, and it
// is only knowable while the tunnel is up. The hub holds it, the scheduler bids with it,
// and the admin list shows it — which is why it lives here rather than in `store` or in
// either generated package.
type Capacity struct {
	Running int
	Max     int
}

// Free is the slot count placement bids with.
func (c Capacity) Free() int { return c.Max - c.Running }
