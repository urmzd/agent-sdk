package agentsdk

// Delta is a sealed interface for streaming incremental updates.
type Delta interface {
	isDelta()
}

// ── Text streaming ──────────────────────────────────────────────────

// TextStartDelta signals the beginning of a text block.
type TextStartDelta struct{}

func (TextStartDelta) isDelta() {}

// TextContentDelta carries an incremental text fragment.
type TextContentDelta struct {
	Content string
}

func (TextContentDelta) isDelta() {}

// TextEndDelta signals the end of a text block.
type TextEndDelta struct{}

func (TextEndDelta) isDelta() {}

// ── Tool call streaming ─────────────────────────────────────────────

// ToolCallStartDelta signals the start of a tool call.
type ToolCallStartDelta struct {
	ID   string
	Name string
}

func (ToolCallStartDelta) isDelta() {}

// ToolCallArgumentDelta carries a JSON fragment of arguments.
type ToolCallArgumentDelta struct {
	Content string
}

func (ToolCallArgumentDelta) isDelta() {}

// ToolCallEndDelta signals the end of a tool call with parsed arguments.
type ToolCallEndDelta struct {
	Arguments map[string]any
}

func (ToolCallEndDelta) isDelta() {}

// ── Terminal deltas ─────────────────────────────────────────────────

// ErrorDelta carries an error from the stream.
type ErrorDelta struct {
	Error error
}

func (ErrorDelta) isDelta() {}

// DoneDelta signals the stream is complete.
type DoneDelta struct{}

func (DoneDelta) isDelta() {}
