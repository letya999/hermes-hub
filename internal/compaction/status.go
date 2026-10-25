package compaction

// Status is the bounded context_status projection: threshold plus provenance,
// observed compaction count, last journaled event, and the current token
// estimate with its provenance. Nil means unknown — never a fabricated zero.
type Status struct {
	ThresholdTokens     *int64 `json:"threshold_tokens"`
	ThresholdProvenance string `json:"threshold_provenance"`
	Compactions         *int64 `json:"compactions"`
	LastEvent           *Event `json:"last_event"`
	Tokens              *int64 `json:"tokens"`
	TokensProvenance    string `json:"tokens_provenance"`
}

// StatusFor projects one session's journal rows into the bounded status
// shape. A session with no journaled compactions reports null count and null
// last_event — absence of evidence is rendered as unknown, not zero.
func (j *Journal) StatusFor(sessionID string, threshold int64, declared bool, tokens *int64, tokensProvenance string) (Status, error) {
	events, err := j.Events(sessionID)
	if err != nil {
		return Status{}, err
	}
	st := Status{Tokens: tokens, TokensProvenance: tokensProvenance, ThresholdProvenance: ProvenanceDefault}
	effective := int64(DefaultSweepThreshold)
	if declared && threshold > 0 {
		effective = threshold
		st.ThresholdProvenance = ProvenanceDeclared
	}
	st.ThresholdTokens = &effective
	if len(events) > 0 {
		committed := int64(0)
		for _, e := range events {
			if e.FailureClass == "" {
				committed++
			}
		}
		st.Compactions = &committed
		last := events[len(events)-1]
		st.LastEvent = &last
	}
	return st, nil
}
