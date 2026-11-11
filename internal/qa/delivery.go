package qa

// DeliveryStep describes one step in a simulated delivery scenario.
type DeliveryStep struct {
	Status       string `json:"status"`
	ErrorCode    int    `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}
