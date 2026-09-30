package config

const ExtraForward = "forward"

// MessageForward is an immutable, bounded transcript of a merged forward.
// It travels in Message.Extra without introducing protocol-specific routing.
type MessageForward struct {
	ID    string
	Nodes []ForwardNode
}

type ForwardNode struct {
	Username string
	Text     string
}

type MentionKind string

const (
	MentionText    MentionKind = "text"
	MentionBot     MentionKind = "bot"
	MentionNative  MentionKind = "mention"
	MentionLiteral MentionKind = "literal"
	MentionUser    MentionKind = "user"
)

// MentionPart distinguishes text, native mentions (including the bot), literal
// rich-message descriptions, and resolved, destination-scoped user mentions.
type MentionPart struct {
	Text    string
	Kind    MentionKind
	UserID  string
	Account string
	Channel string
}

// MessageQuote is a bounded, plain-text preview of a reply's parent. ParentID
// carries the routable identity; this preview survives a missing ID mapping.
type MessageQuote struct {
	ID       string `json:"id,omitempty"`
	Username string `json:"username,omitempty"`
	Text     string `json:"text,omitempty"`
}
