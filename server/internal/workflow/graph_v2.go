package workflow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Ports address top-level values in the input, artifact or explicit join output.
// A collection input receives values in edge order; ordinary inputs have one source.
type Port struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Multiple bool   `json:"multiple,omitempty"`
}
type DataEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"source_port"`
	Target     string `json:"target"`
	TargetPort string `json:"target_port"`
	Order      int    `json:"order"`
}
type Predicate struct {
	InputPort string `json:"input_port"`
	Equals    any    `json:"equals"`
}

func portByID(ports []Port, id string) (Port, bool) {
	for _, p := range ports {
		if p.ID == id {
			return p, true
		}
	}
	return Port{}, false
}
func validPortType(t string) bool {
	switch t {
	case "string", "number", "boolean", "object", "array", "any":
		return true
	}
	return false
}
func valueMatches(t string, value any) bool {
	if value == nil {
		return false
	}
	switch t {
	case "any":
		return true
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		return reflect.ValueOf(value).Kind() == reflect.Slice
	}
	return false
}

func validateGraphV2(v *ValidationErrors, d *Definition, byKey map[string]*Node) {
	validateGraphV2Mode(v, d, byKey, true)
}
func validateGraphV2Mode(v *ValidationErrors, d *Definition, byKey map[string]*Node, complete bool) {
	combinations := 1
	for _, n := range d.Nodes {
		if n.Type == NodeTypeCondition && len(n.Branches) > 0 {
			if combinations > 4096/len(n.Branches) {
				v.add("branches", "condition combinations exceed the static validation limit of 4096")
				break
			}
			combinations *= len(n.Branches)
		}
	}
	ids := map[string]bool{}
	claim := func(id, field string) {
		if id == "" || ids[id] {
			v.add(field, "edge id must be nonempty and unique")
		}
		ids[id] = true
	}
	adjacency := map[string][]string{}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		field := fmt.Sprintf("nodes[%d]", i)
		adjacency[n.Key] = append([]string{}, forwardOutgoing(n)...)
		if len(n.ReworkTargets) > 0 || n.OnFailure == FailurePolicyRework {
			v.add(field, "schema 2 rejects rework cycles; use explicit max_attempts for retries")
		}
		if n.OnFailure != "" && n.OnFailure != FailurePolicyFail {
			v.add(field+".on_failure", "schema 2 propagates final failure; on_failure must be fail or empty")
		}
		if n.JoinPolicy != "" && n.JoinPolicy != JoinPolicyFailFast {
			v.add(field+".join_policy", "schema 2 waits for all activated predecessors")
		}
		if len(n.NextIDs) != len(n.Next) {
			v.add(field+".next_ids", "one stable edge id is required for each next target")
		}
		if complete && n.Routing != nil && n.Routing.FromNode != "" {
			found := false
			for _, source := range d.Nodes {
				if source.Key == n.Routing.FromNode {
					for _, next := range source.Next {
						if next == n.Key {
							found = true
						}
					}
				}
			}
			if !found {
				v.add(field+".routing.from_node", "schema 2 routing source must be a direct ordinary control predecessor")
			}
		}
		seen := map[string]bool{}
		for j, target := range n.Next {
			if seen[target] {
				v.add(field+".next", "duplicate flow edge")
			}
			seen[target] = true
			if j < len(n.NextIDs) {
				claim(n.NextIDs[j], fmt.Sprintf("%s.next_ids[%d]", field, j))
			}
		}
		defaults := 0
		for j, b := range n.Branches {
			branchField := fmt.Sprintf("%s.branches[%d]", field, j)
			claim(b.ID, branchField)
			if b.Predicate == nil && b.WhenVerdict == "" {
				defaults++
			}
			if b.Predicate != nil {
				p, ok := portByID(n.InputPorts, b.Predicate.InputPort)
				if !ok || !valueMatches(p.Type, b.Predicate.Equals) {
					v.add(branchField+".predicate", "predicate requires a declared input port and a compatible comparison value")
				}
			}
		}
		if complete && n.Type == NodeTypeCondition && (defaults != 1 || len(n.Next) != 0) {
			v.add(field+".branches", "condition requires exactly one default branch and no next edges")
		}
		for _, ports := range [][]Port{n.InputPorts, n.OutputPorts} {
			seenPorts := map[string]bool{}
			for _, p := range ports {
				if p.ID == "" || seenPorts[p.ID] || !validPortType(p.Type) {
					v.add(field+".ports", "ports require unique nonempty ids and supported types")
				}
				seenPorts[p.ID] = true
			}
		}
	}
	incoming := map[string][]DataEdge{}
	for edgeIndex, edge := range d.DataEdges {
		field := fmt.Sprintf("data_edges[%d]", edgeIndex)
		claim(edge.ID, field)
		src, dst := byKey[edge.Source], byKey[edge.Target]
		if src == nil || dst == nil {
			v.add(field, "data edge references a missing node")
			continue
		}
		out, outOK := portByID(src.OutputPorts, edge.SourcePort)
		in, inOK := portByID(dst.InputPorts, edge.TargetPort)
		if !outOK || !inOK {
			v.add(field, "data edge must connect a declared output to a declared input")
			continue
		}
		if out.Type != in.Type && in.Type != "any" {
			v.add(field, "incompatible port types")
		}
		key := edge.Target + "/" + edge.TargetPort
		for _, prior := range incoming[key] {
			if !in.Multiple || prior.Order == edge.Order || (prior.Source == edge.Source && prior.SourcePort == edge.SourcePort) {
				v.add(field, "duplicate source, occupied single input, or ambiguous collection order")
			}
		}
		incoming[key] = append(incoming[key], edge)
		adjacency[edge.Source] = append(adjacency[edge.Source], edge.Target)
		if edge.Order < 0 {
			v.add(field, "collection order must be nonnegative")
		}
		// A required source must dominate its consumer in the control graph. A join
		// explicitly merges optional values from mutually exclusive paths instead.
		if complete && in.Required && reachableWithout(d, edge.Target, edge.Source) {
			v.add(field, fmt.Sprintf("required input %s may be activated without source %s; use an explicit merge", key, edge.Source))
		}
	}
	for nodeIndex, n := range d.Nodes {
		for portIndex, p := range n.InputPorts {
			if complete && p.Required && len(incoming[n.Key+"/"+p.ID]) == 0 {
				v.add(fmt.Sprintf("nodes[%d].input_ports[%d]", nodeIndex, portIndex), fmt.Sprintf("required input %s/%s has no source", n.Key, p.ID))
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string)
	visit = func(key string) {
		if visiting[key] {
			v.add("data_edges", "combined flow and data dependencies contain a cycle")
			return
		}
		if visited[key] {
			return
		}
		visiting[key] = true
		for _, next := range adjacency[key] {
			visit(next)
		}
		visiting[key] = false
		visited[key] = true
	}
	for key := range byKey {
		visit(key)
	}
}

// reachableWithout checks bounded condition assignments. Ordinary fan-out activates
// every exit, so a parallel required source is not mistaken for an optional path.
func reachableWithout(d *Definition, target, source string) bool {
	conditions := []Node{}
	for _, n := range d.Nodes {
		if n.Type == NodeTypeCondition {
			conditions = append(conditions, n)
		}
	}
	choice := map[string]string{}
	budget := 4096
	var check func(int) bool
	check = func(i int) bool {
		if budget <= 0 {
			return false
		}
		if i < len(conditions) {
			for _, b := range conditions[i].Branches {
				choice[conditions[i].Key] = b.Target
				if check(i + 1) {
					return true
				}
			}
			return false
		}
		budget--
		seen := map[string]bool{}
		queue := []string{d.EntryNode}
		for len(queue) > 0 {
			key := queue[0]
			queue = queue[1:]
			if seen[key] {
				continue
			}
			seen[key] = true
			if n, ok := d.NodeByKey(key); ok {
				if n.Type == NodeTypeCondition {
					queue = append(queue, choice[key])
				} else {
					queue = append(queue, n.Next...)
				}
			}
		}
		return seen[target] && !seen[source]
	}
	return check(0)
}

// GraphStep is a replayable projection of the latest durable attempt.
type GraphStep struct {
	Status     string
	Attempt    int32
	Output     map[string]any
	SkipReason string
}
type GraphDecision struct {
	Node    string
	Kind    string
	Reason  string
	Attempt int32
	Inputs  map[string]any
}

// PlanGraphV2 never changes state. All callers execute its decisions while
// holding the existing run-row lock, then reload the durable snapshot.
func PlanGraphV2(d *Definition, steps map[string]GraphStep) []GraphDecision {
	var decisions []GraphDecision
	for _, n := range d.Nodes {
		prior, exists := steps[n.Key]
		maxAttempts := n.MaxAttempts
		if maxAttempts == 0 {
			maxAttempts = d.Limits.MaxAttemptsPerNode
		}
		if maxAttempts == 0 {
			maxAttempts = 1
		}
		if exists {
			if (prior.Status == "failed" || prior.Status == "blocked") && prior.Attempt < int32(maxAttempts) && n.Type == NodeTypeAgent {
				inputs, wait, inputError := resolveGraphInputs(d, n, steps)
				if wait || inputError != "" {
					continue
				}
				decisions = append(decisions, GraphDecision{Node: n.Key, Kind: "ready", Attempt: prior.Attempt + 1, Inputs: inputs})
			}
			continue
		}
		pending, active, failed := false, n.Key == d.EntryNode, false
		for _, source := range d.Nodes {
			for _, target := range forwardOutgoing(&source) {
				if target != n.Key {
					continue
				}
				s, ok := steps[source.Key]
				if !ok || !graphTerminal(s.Status) {
					pending = true
					continue
				}
				if (s.Status == "failed" || s.Status == "blocked") && source.Type == NodeTypeAgent {
					limit := source.MaxAttempts
					if limit == 0 {
						limit = d.Limits.MaxAttemptsPerNode
					}
					if limit == 0 {
						limit = 1
					}
					if s.Attempt < int32(limit) && s.SkipReason == "" {
						pending = true
						continue
					}
				}
				if s.Status == "skipped" && s.SkipReason != "dependency_failed" {
					continue
				}
				if source.Type == NodeTypeCondition && s.Status == "passed" && s.Output["target"] != n.Key {
					continue
				}
				active = true
				if s.Status != "passed" {
					failed = true
				}
			}
		}
		if pending {
			continue
		}
		decision := GraphDecision{Node: n.Key, Kind: "ready", Attempt: 1}
		if !active {
			decision.Kind = "skip"
			decision.Reason = "branch_not_selected"
		} else if failed {
			decision.Kind = "skip"
			decision.Reason = "dependency_failed"
		} else {
			inputs, wait, err := resolveGraphInputs(d, n, steps)
			if wait {
				continue
			}
			decision.Inputs = inputs
			if err != "" {
				decision.Kind = "fail"
				decision.Reason = err
			}
		}
		decisions = append(decisions, decision)
	}
	return decisions
}
func graphTerminal(status string) bool {
	switch status {
	case "passed", "failed", "blocked", "skipped", "cancelled":
		return true
	}
	return false
}
func resolveGraphInputs(d *Definition, n Node, steps map[string]GraphStep) (map[string]any, bool, string) {
	values := map[string]any{}
	edges := append([]DataEdge{}, d.DataEdges...)
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].Order < edges[j].Order })
	for _, edge := range edges {
		if edge.Target != n.Key {
			continue
		}
		p, ok := portByID(n.InputPorts, edge.TargetPort)
		if !ok {
			return nil, false, "missing_input_port"
		}
		s, exists := steps[edge.Source]
		if !exists || !graphTerminal(s.Status) {
			return nil, true, ""
		}
		if source, ok := d.NodeByKey(edge.Source); ok && source.Type == NodeTypeAgent && (s.Status == "failed" || s.Status == "blocked") && s.SkipReason == "" {
			limit := source.MaxAttempts
			if limit == 0 {
				limit = d.Limits.MaxAttemptsPerNode
			}
			if limit == 0 {
				limit = 1
			}
			if s.Attempt < int32(limit) {
				return nil, true, ""
			}
		}
		value, has := s.Output[edge.SourcePort]
		if s.Status != "passed" || !has || !valueMatches(p.Type, value) {
			if p.Required {
				return nil, false, "required_input_missing: " + p.ID
			}
			continue
		}
		if p.Multiple {
			list, _ := values[p.ID].([]any)
			values[p.ID] = append(list, value)
		} else {
			values[p.ID] = value
		}
	}
	for _, p := range n.InputPorts {
		if p.Required {
			if _, ok := values[p.ID]; !ok {
				return nil, false, "required_input_missing: " + p.ID
			}
		}
	}
	return values, false, ""
}
func selectGraphBranch(node *Node, inputs map[string]any, verdict string) string {
	fallback := ""
	for _, b := range node.Branches {
		if b.Predicate != nil {
			value, ok := inputs[b.Predicate.InputPort]
			if ok && reflect.DeepEqual(value, b.Predicate.Equals) {
				return b.Target
			}
		} else if b.WhenVerdict == "" {
			fallback = b.Target
		} else if b.WhenVerdict == verdict {
			return b.Target
		}
	}
	return fallback
}
func graphOutput(raw []byte) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// ValidateDraft preserves incomplete authoring but never stores corrupt bindings.
func ValidateDraft(d *Definition, policy WorkspacePolicy, schemas SchemaRegistry) error {
	if d.SchemaVersion != GraphSchemaVersion {
		return Validate(d, policy, schemas)
	}
	v := &ValidationErrors{}
	byKey := map[string]*Node{}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.MaxAttempts < 0 || (policy.MaxAttemptsPerNode > 0 && n.MaxAttempts > policy.MaxAttemptsPerNode) {
			v.add("nodes.max_attempts", "max_attempts must stay within workspace policy")
		}
		if n.Key == "" || byKey[n.Key] != nil || !validNodeTypes[n.Type] {
			v.add("nodes", "invalid or duplicate node identity")
		} else {
			byKey[n.Key] = n
		}
	}
	if v.HasErrors() {
		return v
	}
	for _, n := range d.Nodes {
		for _, target := range n.Next {
			if byKey[target] == nil {
				v.add("nodes.next", "flow edge target does not exist")
			}
		}
		for _, b := range n.Branches {
			if b.Target != "" && byKey[b.Target] == nil {
				v.add("branches", "branch target does not exist")
			}
		}
	}
	validateGraphV2Mode(v, d, byKey, false)
	validateLimits(v, d, policy)
	if v.HasErrors() {
		return v
	}
	return nil
}
