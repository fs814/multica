package centersync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const mergeVersion = 1
const mergeLimit = 10000

// Only content columns are portable. Authentication, integrations, execution
// queues, runtime bindings and permission settings are never selected or written.
type contentTable struct {
	name, columns, keys, scope string
}

var contentTables = []contentTable{
	{"workspace", "id,name,slug,description,context,issue_prefix,avatar_url", "id", "t.id=$1"},
	{"agent", "id,name,description,instructions,avatar_url,model,thinking_level,service_tier,conversation_starters,archived_at", "id", "t.workspace_id=$1 AND t.kind='user'"},
	{"project", "id,title,description,icon,status,priority,start_date,due_date", "id", "t.workspace_id=$1"},
	{"issue_status", "id,key,name,description,category,color,is_system,position,archived_at,icon", "id", "t.workspace_id=$1"},
	{"issue_label", "id,name,color,resource_type,description", "id", "t.workspace_id=$1"},
	{"issue_property", "id,name,type,description,icon,config,position,archived_at", "id", "t.workspace_id=$1"},
	{"skill", "id,name,description,content", "id", "t.workspace_id=$1 AND t.plugin_installation_id IS NULL"},
	{"skill_file", "id,skill_id,path,content", "id", "t.skill_id IN (SELECT id FROM skill WHERE workspace_id=$1 AND plugin_installation_id IS NULL)"},
	{"agent_skill", "agent_id,skill_id,enabled", "agent_id,skill_id", "t.agent_id IN (SELECT id FROM agent WHERE workspace_id=$1 AND kind='user') AND t.skill_id IN (SELECT id FROM skill WHERE workspace_id=$1 AND plugin_installation_id IS NULL)"},
	{"agent_to_label", "agent_id,label_id", "agent_id,label_id", "t.agent_id IN (SELECT id FROM agent WHERE workspace_id=$1 AND kind='user')"},
	{"skill_to_label", "skill_id,label_id", "skill_id,label_id", "t.skill_id IN (SELECT id FROM skill WHERE workspace_id=$1 AND plugin_installation_id IS NULL)"},
	{"squad", "id,name,description,leader_id,avatar_url,instructions,archived_at", "id", "t.workspace_id=$1"},
	{"squad_member", "id,squad_id,member_type,member_id,role", "id", "t.squad_id IN (SELECT id FROM squad WHERE workspace_id=$1)"},
	{"issue", "id,title,description,status,priority,assignee_type,assignee_id,creator_type,creator_id,parent_issue_id,acceptance_criteria,position,due_date,number,project_id,start_date,properties", "id", "t.workspace_id=$1"},
	{"comment", "id,issue_id,author_type,author_id,content,type,parent_id,created_at,resolved_at,resolved_by_type,resolved_by_id,deleted_at", "id", "t.workspace_id=$1"},
	{"issue_to_label", "issue_id,label_id", "issue_id,label_id", "t.issue_id IN (SELECT id FROM issue WHERE workspace_id=$1)"},
	{"issue_dependency", "id,issue_id,depends_on_issue_id,type", "id", "t.issue_id IN (SELECT id FROM issue WHERE workspace_id=$1)"},
	{"chat_session", "id,agent_id,creator_id,title,status,created_at,project_id,model", "id", "t.workspace_id=$1 AND t.agent_id IN (SELECT id FROM agent WHERE workspace_id=$1 AND kind='user')"},
	{"chat_message", "id,chat_session_id,role,content,created_at,message_kind", "id", "t.chat_session_id IN (SELECT c.id FROM chat_session c JOIN agent a ON a.id=c.agent_id WHERE c.workspace_id=$1 AND a.workspace_id=$1 AND a.kind='user')"},
	{"attachment", "id,issue_id,comment_id,chat_session_id,chat_message_id,uploader_type,uploader_id,filename,content_type,size_bytes,created_at,content_sha256", "id", "t.workspace_id=$1 AND t.source_context_id IS NULL AND (t.issue_id IS NOT NULL OR t.chat_session_id IN (SELECT c.id FROM chat_session c JOIN agent a ON a.id=c.agent_id WHERE c.workspace_id=$1 AND a.workspace_id=$1 AND a.kind='user'))"},
}

type contentRecord struct {
	Table  string                     `json:"table"`
	Fields map[string]json.RawMessage `json:"fields"`
}
type contentUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}
type contentFile struct {
	Attachment string `json:"attachment"`
	Data       []byte `json:"data"`
}
type contentBundle struct {
	Version   int             `json:"version"`
	Workspace string          `json:"workspace"`
	Owner     string          `json:"owner"`
	Records   []contentRecord `json:"records"`
	Users     []contentUser   `json:"users"`
	Files     []contentFile   `json:"files"`
}
type mergeConflict struct {
	Table    string          `json:"table"`
	Key      string          `json:"key"`
	Field    string          `json:"field"`
	Local    json.RawMessage `json:"local"`
	Incoming json.RawMessage `json:"incoming"`
}
type mergeResult struct {
	Updated   int             `json:"updated"`
	Conflicts []mergeConflict `json:"conflicts"`
	Workspace string          `json:"workspace"`
}

func tableFor(name string) (contentTable, bool) {
	for _, t := range contentTables {
		if t.name == name {
			return t, true
		}
	}
	return contentTable{}, false
}
func textValue(b json.RawMessage) string { var s string; _ = json.Unmarshal(b, &s); return s }
func rawValue(v any) json.RawMessage     { b, _ := json.Marshal(v); return b }
func equalValue(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}
func recordKey(r contentRecord) string {
	t, ok := tableFor(r.Table)
	if !ok {
		return ""
	}
	var parts []string
	for _, key := range strings.Split(t.keys, ",") {
		id := textValue(r.Fields[key])
		if !validID(id) {
			return ""
		}
		parts = append(parts, id)
	}
	return r.Table + ":" + strings.Join(parts, ":")
}
func canonicalUser(email string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("multica-center-merge-user:"+strings.ToLower(email))).String()
}
func contentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func validateBundle(b contentBundle) error {
	if b.Version != mergeVersion || !validID(b.Workspace) || !validID(b.Owner) || len(b.Records) > mergeLimit || len(b.Users) > mergeLimit || len(b.Files) > mergeLimit {
		return errors.New("invalid workspace merge bundle")
	}
	seen := map[string]bool{}
	attachments := map[string]contentRecord{}
	workspace := false
	for _, r := range b.Records {
		t, ok := tableFor(r.Table)
		key := recordKey(r)
		if !ok || key == "" || seen[key] || len(r.Fields) != len(strings.Split(t.columns, ",")) {
			return errors.New("invalid or duplicate merge record")
		}
		seen[key] = true
		if r.Table == "attachment" {
			attachments[textValue(r.Fields["id"])] = r
		}
		for field, value := range r.Fields {
			if !strings.Contains(","+t.columns+",", ","+field+",") || !json.Valid(value) || len(value) > 1<<20 {
				return errors.New("unselected merge field")
			}
		}
		if r.Table == "workspace" {
			if textValue(r.Fields["id"]) != b.Workspace {
				return errors.New("workspace mismatch")
			}
			workspace = true
		}
	}
	if !workspace {
		return errors.New("workspace metadata missing")
	}
	users := map[string]bool{}
	for _, u := range b.Users {
		address, err := mail.ParseAddress(u.Email)
		if err != nil || address.Address != u.Email || u.ID != canonicalUser(u.Email) || len(u.Email) > 320 || len(u.Name) > 1024 || users[u.ID] || (u.Role != "" && u.Role != "member" && u.Role != "admin" && u.Role != "owner") {
			return errors.New("invalid merge account mapping")
		}
		users[u.ID] = true
	}
	if !users[b.Owner] {
		return errors.New("merge owner missing")
	}
	files := map[string]bool{}
	fileBytes := 0
	for _, f := range b.Files {
		if !seen["attachment:"+f.Attachment] || files[f.Attachment] || len(f.Data) > 8<<20 {
			return errors.New("invalid merge attachment")
		}
		files[f.Attachment] = true
		fileBytes += len(f.Data)
		if fileBytes > 16<<20 {
			return errors.New("workspace attachment capacity exceeded")
		}
		r := attachments[f.Attachment]
		var size int64
		if json.Unmarshal(r.Fields["size_bytes"], &size) != nil || size != int64(len(f.Data)) || textValue(r.Fields["content_sha256"]) != contentHash(f.Data) {
			return errors.New("attachment size or digest mismatch")
		}
	}
	for key := range seen {
		if strings.HasPrefix(key, "attachment:") && !files[strings.TrimPrefix(key, "attachment:")] {
			return errors.New("attachment bytes missing")
		}
	}
	return nil
}

// Existing schemas still have historical parent foreign keys. Preserve their
// insertion order without temporarily writing broken relationships.
func orderedRecords(records []contentRecord) ([]contentRecord, error) {
	var result []contentRecord
	for _, table := range contentTables {
		byID := map[string]contentRecord{}
		state := map[string]int{}
		for _, r := range records {
			if r.Table == table.name {
				byID[recordKey(r)] = r
			}
		}
		var visit func(string) error
		visit = func(key string) error {
			if state[key] == 2 {
				return nil
			}
			if state[key] == 1 {
				return errors.New("cyclic content parent relationship")
			}
			r, ok := byID[key]
			if !ok {
				return errors.New("content parent missing")
			}
			state[key] = 1
			parent := ""
			if table.name == "issue" {
				parent = textValue(r.Fields["parent_issue_id"])
			}
			if table.name == "comment" {
				parent = textValue(r.Fields["parent_id"])
			}
			if parent != "" {
				if err := visit(table.name + ":" + parent); err != nil {
					return err
				}
			}
			state[key] = 2
			result = append(result, r)
			return nil
		}
		keys := make([]string, 0, len(byID))
		for key := range byID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := visit(key); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// A missing record is never interpreted as permission to delete or resurrect.
// Conflicted fields retain both values and their previous shared baseline.
func mergeFields(local, incoming, base map[string]json.RawMessage, table, key string) (map[string]json.RawMessage, map[string]json.RawMessage, []mergeConflict) {
	merged, next := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for k, v := range local {
		merged[k] = v
	}
	for k, v := range base {
		next[k] = v
	}
	var conflicts []mergeConflict
	keys := make([]string, 0, len(incoming))
	for k := range incoming {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := incoming[k]
		current, exists := local[k]
		previous, known := base[k]
		switch {
		case !exists && local == nil && base == nil:
			merged[k] = v
			next[k] = v
		case exists && equalValue(current, v):
			next[k] = v
		case known && exists && equalValue(current, previous):
			merged[k] = v
			next[k] = v
		case known && equalValue(v, previous):
		default:
			conflicts = append(conflicts, mergeConflict{table, key, k, current, v})
		}
	}
	// Actor kind/identity and attachment digest/length are indivisible values.
	// Field-wise merging could otherwise invent an actor or mislabel local bytes.
	groups := [][]string{{"content_sha256", "size_bytes"}}
	for _, prefix := range []string{"assignee", "creator", "author", "resolved_by", "uploader", "member"} {
		groups = append(groups, []string{prefix + "_type", prefix + "_id"})
	}
	for _, group := range groups {
		if _, ok := incoming[group[0]]; !ok {
			continue
		}
		pick := func(fields map[string]json.RawMessage) json.RawMessage {
			if fields == nil {
				return rawValue(nil)
			}
			value := map[string]json.RawMessage{}
			for _, k := range group {
				value[k] = fields[k]
			}
			return rawValue(value)
		}
		l, i, b := pick(local), pick(incoming), pick(base)
		chosen, baseline := local, base
		conflict := false
		switch {
		case local == nil && base == nil, equalValue(l, i), base != nil && equalValue(l, b):
			chosen, baseline = incoming, incoming
		case base != nil && equalValue(i, b):
		default:
			conflict = true
		}
		kept := conflicts[:0]
		for _, c := range conflicts {
			if c.Field != group[0] && c.Field != group[1] {
				kept = append(kept, c)
			}
		}
		conflicts = kept
		for _, k := range group {
			merged[k] = chosen[k]
			delete(next, k)
			if value, ok := baseline[k]; ok {
				next[k] = value
			}
		}
		if conflict {
			conflicts = append(conflicts, mergeConflict{table, key, "$" + group[0], l, i})
		}
	}
	return merged, next, conflicts
}
