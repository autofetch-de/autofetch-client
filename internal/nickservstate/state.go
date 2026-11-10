// Package nickservstate defines evidence-based states shared by the manual
// NickServ protocol, durable client settings, and both local user interfaces.
package nickservstate

type Status string

const (
	// Unconfirmed means no conclusive response was observed. The account might
	// have been created, so do not automatically issue another REGISTER.
	Unconfirmed Status = "unconfirmed"
	// Failed means NickServ or the IRC server explicitly rejected the action.
	Failed Status = "failed"
	// AwaitingConfirmation means NickServ explicitly requested an email/code step.
	AwaitingConfirmation Status = "awaiting_confirmation"
	// RegisteredUnverified means NickServ acknowledged registration, but the
	// subsequent IDENTIFY has not been positively acknowledged.
	RegisteredUnverified Status = "registered_unverified"
	// Identified is reserved for a positive NickServ identification response.
	Identified Status = "identified"
)
