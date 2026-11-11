package send

type PollRequest struct {
	BaseRequest
	Question  string   `json:"question" form:"question"`
	Options   []string `json:"options" form:"options"`
	MaxAnswer int      `json:"max_answer" form:"max_answer"`
}

// PollVoteRequest votes on an existing poll. Empty Options retracts the vote.
type PollVoteRequest struct {
	Phone     string   `json:"phone" form:"phone"`
	MessageID string   `json:"message_id" form:"message_id"`
	Options   []string `json:"options" form:"options"`
}
