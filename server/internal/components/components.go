// Package components validates and stores the A2UI surfaces agents emit.
//
// Every surface (web, Teams, iOS, Android) renders the same Workbench
// catalogue, so the server refuses anything outside it: an agent cannot ship
// a component some client cannot draw, and nothing an agent emits is
// executable.
package components

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Catalog is the catalogue document, a copy of /catalogue/catalog.json kept
// in step by a test.
//
//go:embed catalog.json
var Catalog []byte

// Version is the A2UI version Workbench speaks.
const Version = "v0.9.1"

// MimeType marks an A2A DataPart that carries A2UI messages.
const MimeType = "application/a2ui+json"

// Limits keep a surface small enough for every renderer, including Teams
// Adaptive Cards and phones.
const (
	MaxMessages   = 20
	MaxComponents = 120
	MaxBytes      = 64 << 10
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9_\-:.]{1,128}$`)

// commonProps may appear on any component.
var commonProps = []string{"id", "component", "weight", "accessibility"}

type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	Enum                 []any              `json:"enum"`
	Pattern              string             `json:"pattern"`
	MinLength            *int               `json:"minLength"`
	MaxLength            *int               `json:"maxLength"`
	MaxItems             *int               `json:"maxItems"`
	Items                *schema            `json:"items"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	OneOf                []*schema          `json:"oneOf"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	re                   *regexp.Regexp
}

type componentDef struct {
	Description string             `json:"description"`
	Properties  map[string]*schema `json:"properties"`
	Required    []string           `json:"required"`
}

// Validator checks A2UI messages against the catalogue.
type Validator struct {
	CatalogID  string
	defs       map[string]*schema
	components map[string]componentDef
}

// NewValidator parses the embedded catalogue.
func NewValidator() (*Validator, error) { return Parse(Catalog) }

// Parse builds a validator from a catalogue document.
func Parse(doc []byte) (*Validator, error) {
	var raw struct {
		CatalogID  string                  `json:"catalogId"`
		Defs       map[string]*schema      `json:"$defs"`
		Components map[string]componentDef `json:"components"`
	}
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}
	if raw.CatalogID == "" || len(raw.Components) == 0 {
		return nil, fmt.Errorf("catalogue: missing catalogId or components")
	}
	v := &Validator{CatalogID: raw.CatalogID, defs: raw.Defs, components: raw.Components}
	var compile func(*schema) error
	compile = func(s *schema) error {
		if s == nil {
			return nil
		}
		if s.Pattern != "" {
			re, err := regexp.Compile(s.Pattern)
			if err != nil {
				return err
			}
			s.re = re
		}
		for _, c := range append(append([]*schema{s.Items}, s.OneOf...), values(s.Properties)...) {
			if err := compile(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range raw.Defs {
		if err := compile(d); err != nil {
			return nil, err
		}
	}
	for _, c := range raw.Components {
		for _, p := range c.Properties {
			if err := compile(p); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

func values(m map[string]*schema) []*schema {
	out := make([]*schema, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// Components lists the catalogue's component names.
func (v *Validator) Components() []string {
	out := make([]string, 0, len(v.components))
	for k := range v.components {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Batch is a validated set of messages for one surface.
type Batch struct {
	SurfaceID string
	Messages  []map[string]any
	Deleted   bool
	// Creates is true when the batch opens the surface.
	Creates bool
}

// Validate checks a list of A2UI messages. All messages must address one
// surface. It returns the batch normalised (version stamped on every message).
func (v *Validator) Validate(messages []map[string]any) (Batch, error) {
	var b Batch
	if len(messages) == 0 {
		return b, fmt.Errorf("no A2UI messages")
	}
	if len(messages) > MaxMessages {
		return b, fmt.Errorf("at most %d messages per call", MaxMessages)
	}
	if raw, _ := json.Marshal(messages); len(raw) > MaxBytes {
		return b, fmt.Errorf("A2UI messages exceed %d bytes", MaxBytes)
	}
	components := 0
	for i, m := range messages {
		if ver, ok := m["version"]; ok && ver != Version {
			return b, fmt.Errorf("message %d: version %v; Workbench speaks %s", i, ver, Version)
		}
		kinds := []string{}
		for _, k := range []string{"createSurface", "updateComponents", "updateDataModel", "deleteSurface"} {
			if _, ok := m[k]; ok {
				kinds = append(kinds, k)
			}
		}
		for k := range m {
			if k != "version" && !slices.Contains([]string{"createSurface", "updateComponents", "updateDataModel", "deleteSurface"}, k) {
				return b, fmt.Errorf("message %d: unknown field %q", i, k)
			}
		}
		if len(kinds) != 1 {
			return b, fmt.Errorf("message %d: needs exactly one of createSurface, updateComponents, updateDataModel, deleteSurface", i)
		}
		body, ok := m[kinds[0]].(map[string]any)
		if !ok {
			return b, fmt.Errorf("message %d: %s must be an object", i, kinds[0])
		}
		sid, _ := body["surfaceId"].(string)
		if !idRE.MatchString(sid) {
			return b, fmt.Errorf("message %d: surfaceId must be 1-128 letters, digits, _ - : .", i)
		}
		if b.SurfaceID == "" {
			b.SurfaceID = sid
		} else if b.SurfaceID != sid {
			return b, fmt.Errorf("message %d: all messages in one call must address one surface", i)
		}
		switch kinds[0] {
		case "createSurface":
			if cat, _ := body["catalogId"].(string); cat != v.CatalogID {
				return b, fmt.Errorf("message %d: catalogId must be %s", i, v.CatalogID)
			}
			b.Creates = true
			if comps, ok := body["components"]; ok { // 1.0 style inline components
				n, err := v.validateComponents(comps)
				if err != nil {
					return b, fmt.Errorf("message %d: %w", i, err)
				}
				components += n
			}
		case "updateComponents":
			n, err := v.validateComponents(body["components"])
			if err != nil {
				return b, fmt.Errorf("message %d: %w", i, err)
			}
			components += n
		case "updateDataModel":
			if p, ok := body["path"]; ok {
				if ps, _ := p.(string); !strings.HasPrefix(ps, "/") {
					return b, fmt.Errorf("message %d: updateDataModel path must be a JSON pointer", i)
				}
			}
		case "deleteSurface":
			b.Deleted = true
		}
		m["version"] = Version
	}
	if components > MaxComponents {
		return b, fmt.Errorf("a surface may have at most %d components", MaxComponents)
	}
	b.Messages = messages
	return b, nil
}

func (v *Validator) validateComponents(raw any) (int, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return 0, fmt.Errorf("components must be a non-empty array")
	}
	ids := map[string]bool{}
	for j, item := range list {
		c, ok := item.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("component %d is not an object", j)
		}
		id, _ := c["id"].(string)
		if !idRE.MatchString(id) {
			return 0, fmt.Errorf("component %d: bad id", j)
		}
		if ids[id] {
			return 0, fmt.Errorf("component id %q used twice", id)
		}
		ids[id] = true
		name, _ := c["component"].(string)
		def, ok := v.components[name]
		if !ok {
			return 0, fmt.Errorf("component %q: %q is not in the Workbench catalogue (%s)", id, name, strings.Join(v.Components(), ", "))
		}
		for _, req := range def.Required {
			if _, ok := c[req]; !ok {
				return 0, fmt.Errorf("component %q (%s): missing %q", id, name, req)
			}
		}
		for k, val := range c {
			if slices.Contains(commonProps, k) {
				continue
			}
			ps, ok := def.Properties[k]
			if !ok {
				return 0, fmt.Errorf("component %q (%s): unknown property %q", id, name, k)
			}
			if err := v.check(ps, val, name+"."+k); err != nil {
				return 0, fmt.Errorf("component %q: %w", id, err)
			}
		}
	}
	return len(list), nil
}

func (v *Validator) resolve(s *schema) *schema {
	for s != nil && s.Ref != "" {
		s = v.defs[strings.TrimPrefix(s.Ref, "#/$defs/")]
	}
	return s
}

func (v *Validator) check(s *schema, val any, at string) error {
	s = v.resolve(s)
	if s == nil {
		return fmt.Errorf("%s: no schema", at)
	}
	if len(s.OneOf) > 0 {
		for _, alt := range s.OneOf {
			if v.check(alt, val, at) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s: value does not match any allowed form", at)
	}
	if len(s.Enum) > 0 {
		if !slices.ContainsFunc(s.Enum, func(e any) bool { return e == val }) {
			return fmt.Errorf("%s: %v is not one of %v", at, val, s.Enum)
		}
		return nil
	}
	switch s.Type {
	case "string":
		str, ok := val.(string)
		if !ok {
			return fmt.Errorf("%s: must be a string", at)
		}
		if s.MinLength != nil && len(str) < *s.MinLength || s.MaxLength != nil && len(str) > *s.MaxLength {
			return fmt.Errorf("%s: wrong length", at)
		}
		if s.re != nil && !s.re.MatchString(str) {
			return fmt.Errorf("%s: %q does not match %s", at, str, s.Pattern)
		}
	case "number":
		if _, ok := val.(float64); !ok {
			return fmt.Errorf("%s: must be a number", at)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("%s: must be an array", at)
		}
		if s.MaxItems != nil && len(arr) > *s.MaxItems {
			return fmt.Errorf("%s: at most %d items", at, *s.MaxItems)
		}
		for i, item := range arr {
			if err := v.check(s.Items, item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case "object":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", at)
		}
		for _, r := range s.Required {
			if _, ok := obj[r]; !ok {
				return fmt.Errorf("%s: missing %q", at, r)
			}
		}
		for k, pv := range obj {
			ps, ok := s.Properties[k]
			if !ok {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("%s: unknown property %q", at, k)
				}
				continue
			}
			if err := v.check(ps, pv, at+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}
