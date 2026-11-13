package goals

import (
	"context"
	"fmt"
	"strings"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// draftObjectiveLimit is the rune limit of the objective in the draft prompt, like Hermes.
const draftObjectiveLimit = 4000

// DraftContract asks the judge model to turn objective into a completion contract. It
// returns false when the objective is empty, the call fails, the reply is not one JSON
// object, or the contract is empty. Hermes sends temperature 0. provider.Request has no
// temperature field, so the call sends none.
func DraftContract(ctx context.Context, j *Judge, prov provider.Provider, objective string) (Contract, bool) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return Contract{}, false
	}
	limit := j.DraftChars
	if limit <= 0 {
		limit = draftObjectiveLimit
	}
	res, err := j.Call(ctx, prov, DraftContractSystemPrompt, "Objective:\n"+cutRunes(objective, limit))
	if err != nil {
		return Contract{}, false
	}
	data := extractJSONObject(res.Text)
	if data == nil {
		return Contract{}, false
	}
	var c Contract
	for _, key := range contractFields {
		// Hermes GoalContract.from_dict uses str(data.get(f) or ""). A false value, for example
		// null, 0, or an empty list, gives "", and str() converts a value that is not a string.
		v := data[key]
		if !pyTruthy(v) {
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			s = fmt.Sprint(v)
		}
		*c.field(key) = strings.TrimSpace(s)
	}
	if c.Empty() {
		return Contract{}, false
	}
	return c, true
}
