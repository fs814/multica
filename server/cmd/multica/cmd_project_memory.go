package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"github.com/spf13/cobra"
	"os"
	"sort"
	"time"
	"unicode/utf8"
)

func init() {
	memory := &cobra.Command{Use: "memory", Short: "Read and publish persistent project knowledge"}
	projectCmd.AddCommand(memory)
	for _, action := range []string{"resolve", "init", "list", "read", "write", "delete", "migrate", "import"} {
		action := action
		cmd := &cobra.Command{Use: action + " <project-id>", Short: action + " project memory", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runProjectMemory(cmd, args, action) }}
		cmd.Flags().String("path", "", "Relative memory entry")
		cmd.Flags().String("content-file", "", "UTF-8 file to publish")
		cmd.Flags().String("source", "", "Evidence or source reference for a write")
		cmd.Flags().Int64("expected-revision", -1, "Required content revision for mutations")
		cmd.Flags().Int64("expected-binding-revision", -1, "Required binding revision for mutations")
		cmd.Flags().String("owner-runtime", "", "Initial owner runtime UUID (human only)")
		cmd.Flags().String("destination", "", "New absolute source root; empty selects managed storage")
		memory.AddCommand(cmd)
	}
}
func runProjectMemory(cmd *cobra.Command, args []string, action string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 65*time.Second)
	defer cancel()
	project, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return err
	}
	base := "/api/projects/" + project.ID + "/memory"
	var b projectmemory.Binding
	err = client.GetJSON(ctx, base, &b)
	if action == "resolve" {
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
	}
	owner, _ := cmd.Flags().GetString("owner-runtime")
	if err != nil && !(action == "init" && owner != "") {
		return err
	}
	op := projectmemory.Operation{Action: action, OwnerRuntimeID: owner}
	op.Path, _ = cmd.Flags().GetString("path")
	op.Source, _ = cmd.Flags().GetString("source")
	op.Destination, _ = cmd.Flags().GetString("destination")
	op.ExpectedRevision, _ = cmd.Flags().GetInt64("expected-revision")
	op.ExpectedBindingRevision, _ = cmd.Flags().GetInt64("expected-binding-revision")
	if action == "read" || action == "list" {
		op.Action = "read"
		op.ExpectedRevision = b.ContentRevision
		op.ExpectedBindingRevision = b.Revision
	} else {
		if op.ExpectedRevision < 0 || op.ExpectedBindingRevision < 1 {
			return fmt.Errorf("mutations require --expected-revision and --expected-binding-revision")
		}
		if op.Source == "" {
			return fmt.Errorf("mutations require --source")
		}
	}
	if action == "write" || action == "import" {
		filename, _ := cmd.Flags().GetString("content-file")
		if filename == "" {
			return fmt.Errorf("write requires --content-file")
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("memory content must be UTF-8")
		}
		if len(data) > projectmemory.MaxBytes {
			return fmt.Errorf("memory content exceeds limit")
		}
		if action == "import" {
			if err = json.Unmarshal(data, &op.Files); err != nil {
				return fmt.Errorf("import expects a JSON object mapping relative file names to UTF-8 text: %w", err)
			}
		} else {
			op.Content = string(data)
		}
	}
	var work projectmemory.Work
	if err = client.PostJSON(ctx, base+"/operations", op, &work); err != nil {
		return err
	}
	for {
		var receipt projectmemory.Receipt
		if err = client.GetJSON(ctx, base+"/operations/"+work.ID, &receipt); err != nil {
			return err
		}
		switch receipt.Status {
		case "done":
			if action == "list" {
				names := []string{}
				for name := range receipt.Result.Snapshot.Files {
					names = append(names, name)
				}
				sort.Strings(names)
				return json.NewEncoder(cmd.OutOrStdout()).Encode(names)
			}
			if action == "read" && op.Path != "" {
				text, err := projectmemory.ReadFile(*receipt.Result.Snapshot, op.Path)
				if err != nil {
					return err
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), text)
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt.Result)
		case "failed", "unavailable":
			return fmt.Errorf("memory %s: %v", receipt.Status, receipt.Result)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owner unavailable; operation %s was not confirmed: %w", work.ID, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}
