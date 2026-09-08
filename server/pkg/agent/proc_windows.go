//go:build windows

package agent

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNewConsole allocates a fresh console for the child process. Combined
// with HideWindow=true (STARTF_USESHOWWINDOW + SW_HIDE) the console window
// stays off-screen, and — critically — any grandchildren the agent spawns
// (tool subprocesses like bash, cmd, netstat, findstr) inherit this hidden
// console instead of each allocating their own visible one.
//
// Using CREATE_NO_WINDOW here instead would strip the console entirely,
// which forces Windows to allocate a new visible console per grandchild
// when the grandchild is a console-subsystem program that doesn't itself
// pass CREATE_NO_WINDOW — the exact popup storm reported in #1521.
const createNewConsole = 0x00000010

// hideAgentWindow configures cmd to suppress the console window on Windows
// while still giving descendant processes a hidden console to inherit.
// Stdio pipes set via cmd.StdoutPipe/StdinPipe keep working because
// STARTF_USESTDHANDLES takes precedence over the new console's stdio.
func hideAgentWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNewConsole
}

// configureProcessGroup is a no-op on Windows: there is no Setpgid, and
// CREATE_NEW_PROCESS_GROUP is silently ignored when combined with the
// CREATE_NEW_CONSOLE that hideAgentWindow must set (#1521). Windows tree
// ownership can only be established *after* the child exists, because a Job
// Object assignment needs a live pid, so it lives in startAgentProcess.
func configureProcessGroup(cmd *exec.Cmd) {}

// jobObjectBasicAccountingInformation mirrors JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
// x/sys exposes QueryInformationJobObject and the information-class constant,
// but not this result struct.
type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// processTree owns one agent invocation's Job Object. Every descendant the
// agent spawns (MCP servers, plus the sh.exe/cmd.exe tool subprocesses that
// were leaking) becomes a job member, so the whole tree can be terminated and
// positively observed as gone.
type processTree struct {
	job    windows.Handle
	leader windows.Handle

	// reaped is set once the tree has been confirmed empty and the handles
	// released. The record is kept (as a tombstone) for reapedRetention
	// afterwards so a caller that asks "is the tree gone?" just after the
	// leader exited gets a truthful "yes" instead of an ambiguous "no record".
	reaped bool
}

// reapedRetention is how long a reaped tree's tombstone stays queryable.
// Callers ask right after cmd.Wait() returns, so this only has to outlive the
// gap between the reaper finishing and the caller asking.
const reapedRetention = 2 * time.Minute

var (
	processTreesMu sync.Mutex
	processTrees   = map[int]*processTree{}
)

func lookupProcessTree(pid int) *processTree {
	processTreesMu.Lock()
	defer processTreesMu.Unlock()
	return processTrees[pid]
}

// startAgentProcess starts cmd and places it — and everything it goes on to
// spawn — inside a Job Object.
//
// Why not a flag on SysProcAttr: Go's Windows SysProcAttr exposes no job
// attribute, so a job cannot be applied at creation time. Assignment therefore
// happens immediately after Start. That leaves a very small window in which
// the child could spawn a descendant that escapes the job; in practice agent
// CLIs spawn nothing until they have read a prompt from stdin, which callers
// write only after this function returns.
//
// Job creation/assignment failure is deliberately non-fatal: losing tree
// containment is strictly better than refusing to run the task, and the
// pre-existing leader-only kill still applies.
func startAgentProcess(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	attachProcessTree(cmd)
	return nil
}

func attachProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	// KILL_ON_JOB_CLOSE makes the final CloseHandle a tree kill, so even a
	// panicking daemon cannot strand descendants.
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return
	}

	// SYNCHRONIZE is required so reapWhenLeaderExits can wait on this handle.
	// Holding the handle also pins the pid: Windows cannot recycle it while an
	// open handle exists, so the reaper can never act on a reused pid.
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(process)
		_ = windows.CloseHandle(job)
		return
	}

	tree := &processTree{job: job, leader: process}
	processTreesMu.Lock()
	processTrees[pid] = tree
	processTreesMu.Unlock()

	go tree.reapWhenLeaderExits(pid)
}

// reapWhenLeaderExits handles the orphan-on-normal-completion case.
// Cancellation paths call signalProcessGroup explicitly, but a task that ended
// *successfully* used to leave descendants running: the leader exited, the
// daemon moved on, and a tool subprocess holding a severed pipe spun at ~100%
// kernel time indefinitely while still holding the per-task TEMP directory
// open. Waiting on the leader handle costs nothing in the common case, and
// terminating the job afterwards guarantees the tree is gone.
func (t *processTree) reapWhenLeaderExits(pid int) {
	defer func() {
		processTreesMu.Lock()
		// Mark reaped rather than deleting outright: waitProcessGroupGone
		// cannot distinguish "never tracked" from "already cleaned up", and
		// deleting immediately made a successful cleanup look like a failure.
		t.reaped = true
		processTreesMu.Unlock()
		_ = windows.CloseHandle(t.leader)
		// Releasing the last job handle triggers KILL_ON_JOB_CLOSE, which
		// terminates anything that somehow survived the explicit terminate.
		_ = windows.CloseHandle(t.job)

		time.AfterFunc(reapedRetention, func() {
			processTreesMu.Lock()
			if processTrees[pid] == t {
				delete(processTrees, pid)
			}
			processTreesMu.Unlock()
		})
	}()

	_, _ = windows.WaitForSingleObject(t.leader, windows.INFINITE)

	// The leader is gone, so anything still in the job is an orphan by
	// definition. Terminate the job and confirm it drained.
	if active, err := t.activeProcesses(); err == nil && active == 0 {
		return
	}
	_ = windows.TerminateJobObject(t.job, 1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		active, err := t.activeProcesses()
		if err != nil || active == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (t *processTree) activeProcesses() (uint32, error) {
	var info jobObjectBasicAccountingInformation
	if err := windows.QueryInformationJobObject(
		t.job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return 0, err
	}
	return info.ActiveProcesses, nil
}

// codexInitializeRetrySupported stays false for now. Descendant termination is
// confirmable as of this change, but re-enabling the Codex initialize retry is
// a behavioural change to Codex startup that deserves its own validation.
func codexInitializeRetrySupported() bool { return false }

// signalProcessGroup maps the caller's POSIX escalation ladder onto Windows.
//
// SIGTERM keeps its historical meaning here — terminate the leader only — so
// the caller's graceful window still belongs to the agent CLI, which may flush
// state and shut its own children down. SIGKILL is the escalation and
// terminates the entire Job Object, which is what finally reaches the tool
// subprocesses that a leader-only TerminateProcess left behind.
func signalProcessGroup(p *os.Process, sig syscall.Signal) {
	if p == nil {
		return
	}
	if sig == syscall.SIGKILL {
		if tree := lookupProcessTree(p.Pid); tree != nil {
			processTreesMu.Lock()
			reaped := tree.reaped
			processTreesMu.Unlock()
			// Already reaped: the tree is gone and the job handle is closed.
			if reaped {
				return
			}
			if err := windows.TerminateJobObject(tree.job, 1); err == nil {
				return
			}
		}
	}
	_ = p.Kill()
}

// waitProcessGroupGone reports whether every member of the tree has exited.
// This previously returned false unconditionally on Windows, which forced
// callers to assume the worst and made positive cleanup unverifiable. With a
// Job Object the answer is authoritative: ActiveProcesses counts live members.
func waitProcessGroupGone(p *os.Process, timeout time.Duration) bool {
	if p == nil {
		return false
	}
	tree := lookupProcessTree(p.Pid)
	if tree == nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		processTreesMu.Lock()
		reaped := tree.reaped
		processTreesMu.Unlock()
		// Reaped means the reaper already confirmed the job empty and closed
		// the handles; querying them now would fail on an invalid handle.
		if reaped {
			return true
		}
		active, err := tree.activeProcesses()
		if err != nil {
			return false
		}
		if active == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
