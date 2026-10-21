package events

import "time"

// Marco 12 event type: the account's business profile changed through the API.
const TypeBusinessProfileUpdated = "business.profile_updated"

func init() { Types = append(Types, TypeBusinessProfileUpdated) }

// BusinessProfileUpdated is the data of business.profile_updated: which fields the API
// changed and the profile read back from WhatsApp afterwards.
type BusinessProfileUpdated struct {
	Fields    []string  `json:"fields"` // description | address | email | websites | location | categories | hours
	Profile   any       `json:"profile"`
	Source    string    `json:"source"` // api
	Timestamp time.Time `json:"timestamp"`
}
