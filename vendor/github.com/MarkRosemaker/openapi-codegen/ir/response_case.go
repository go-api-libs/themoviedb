package ir

import (
	"slices"
	"strings"
)

// ResponseCase is one case of the switch on the status of a response: Response, and Also, the responses after it
// that the client handles alike, so that it writes the code for them once.
type ResponseCase struct {
	Response
	Also []Response `json:"also,omitempty"`
}

// Cases is the responses as the cases of the switch on the status, in the order a status is matched against them
// (see [Responses.InMatchOrder]), those the client handles alike in one: a case for codes takes any code alike, since
// codes match one each, and one for ranges only the response right after it, as a range must not come before a code
// in it. The default response is a case of its own.
func (rs Responses) Cases() []ResponseCase {
	var cases []ResponseCase

	byRange := rs.HasRange()

next:
	for _, r := range rs.InMatchOrder() {
		for i := range cases {
			if byRange && i < len(cases)-1 {
				continue
			}

			if cases[i].alike(r) {
				cases[i].Also = append(cases[i].Also, r)
				continue next
			}
		}

		cases = append(cases, ResponseCase{Response: r})
	}

	return cases
}

// alike reports whether the client handles r as it does c's responses: neither is the default, and both are a
// success, or neither is, with the same body.
func (c ResponseCase) alike(r Response) bool {
	a := c.Response

	return a.StatusCode != "default" && r.StatusCode != "default" &&
		a.IsSuccess == r.IsSuccess && a.ContentType == r.ContentType &&
		a.IsRawBytes == r.IsRawBytes && a.IsStream == r.IsStream &&
		(a.GoType == nil) == (r.GoType == nil) && (a.GoType == nil || *a.GoType == *r.GoType)
}

// all is the responses of c.
func (c ResponseCase) all() []Response { return append([]Response{c.Response}, c.Also...) }

// GoConstants is the codes of the case, for a switch on the code.
func (c ResponseCase) GoConstants() string {
	var consts []string
	for _, r := range c.all() {
		consts = append(consts, r.GoConstant)
	}

	return strings.Join(consts, ", ")
}

// Conditions is the condition of the case, for a switch on no value: any of its responses' conditions.
func (c ResponseCase) Conditions() string {
	var conds []string
	for _, r := range c.all() {
		conds = append(conds, r.Condition())
	}

	return strings.Join(conds, " || ")
}

// Descriptions is the descriptions of the responses of the case, each once, a line each.
func (c ResponseCase) Descriptions() string {
	var descs []string
	for _, r := range c.all() {
		if r.Description != "" && !slices.Contains(descs, r.Description) {
			descs = append(descs, r.Description)
		}
	}

	return strings.Join(descs, "\n")
}
