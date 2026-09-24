# Own a Windows Job before the supervisor can spawn any descendants. Closing
# the handle kills only this launch, even if a wrapper has already exited.
param([Parameter(Mandatory=$true)][string]$Node, [Parameter(Mandatory=$true)][string]$Script)
$ErrorActionPreference = 'Stop'
Add-Type @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
public static class CenterJob {
  [StructLayout(LayoutKind.Sequential)] struct IO_COUNTERS { public ulong a,b,c,d,e,f; }
  [StructLayout(LayoutKind.Sequential)] struct BASIC {
    public long a,b; public uint flags; public UIntPtr min,max; public uint limit;
    public UIntPtr affinity; public uint priority,scheduling;
  }
  [StructLayout(LayoutKind.Sequential)] struct EXTENDED {
    public BASIC basic; public IO_COUNTERS io; public UIntPtr processMemory,jobMemory,peakProcess,peakJob;
  }
  [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Unicode)] struct STARTUP {
    public uint cb; public string reserved,desktop,title;
    public uint x,y,xsize,ysize,xchars,ychars,fill,flags;
    public ushort show,reserved2; public IntPtr reservedPtr,input,output,error;
  }
  [StructLayout(LayoutKind.Sequential)] struct PROCESS { public IntPtr process,thread; public uint pid,tid; }
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr a,string name);
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job,int cls,ref EXTENDED info,uint size);
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool CreateProcess(string app,System.Text.StringBuilder cmd,IntPtr pa,IntPtr ta,bool inherit,uint flags,IntPtr env,string cwd,ref STARTUP si,out PROCESS pi);
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job,IntPtr process);
  [DllImport("kernel32.dll")] static extern uint ResumeThread(IntPtr thread);
  [DllImport("kernel32.dll")] static extern uint WaitForSingleObject(IntPtr handle,uint ms);
  [DllImport("kernel32.dll")] static extern bool GetExitCodeProcess(IntPtr process,out uint code);
  [DllImport("kernel32.dll")] static extern bool TerminateProcess(IntPtr process,uint code);
  [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
  delegate bool Handler(uint signal);
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetConsoleCtrlHandler(Handler handler,bool add);
  // Keep the delegate alive. Unlike ignore-Ctrl+C, a handler is not inherited
  // by the child. The child still receives the real console event.
  static volatile bool interrupted;
  static readonly Handler handler = signal => {
    if(signal != 0 && signal != 1) return false;
    interrupted = true;
    return true; // Do not let PowerShell cancel the pipeline before `exit`.
  };
  static void Check(bool ok) { if(!ok) throw new Win32Exception(Marshal.GetLastWin32Error()); }
  public static int Run(string node,string script) {
    interrupted=false;
    Check(SetConsoleCtrlHandler(handler,true));
    IntPtr job=CreateJobObject(IntPtr.Zero,null); Check(job!=IntPtr.Zero);
    PROCESS pi=new PROCESS();
    try {
      EXTENDED info=new EXTENDED(); info.basic.flags=0x2000; // KILL_ON_JOB_CLOSE
      Check(SetInformationJobObject(job,9,ref info,(uint)Marshal.SizeOf(info)));
      STARTUP si=new STARTUP(); si.cb=(uint)Marshal.SizeOf(si);
      // Both arguments are file paths, which cannot contain a double quote.
      var cmd=new System.Text.StringBuilder("\""+node+"\" \""+script+"\" --owned-job");
      Check(CreateProcess(node,cmd,IntPtr.Zero,IntPtr.Zero,true,4,IntPtr.Zero,null,ref si,out pi));
      Check(AssignProcessToJobObject(job,pi.process));
      Check(ResumeThread(pi.thread)!=0xffffffff);
      long cancelDeadline=0;
      while(true) {
        uint wait=WaitForSingleObject(pi.process,100);
        if(wait==0) break;
        if(wait!=258) throw new Win32Exception(Marshal.GetLastWin32Error());
        if(interrupted) {
          if(cancelDeadline==0) cancelDeadline=System.Diagnostics.Stopwatch.GetTimestamp()/ (System.Diagnostics.Stopwatch.Frequency/1000)+8000;
          if(System.Diagnostics.Stopwatch.GetTimestamp()/ (System.Diagnostics.Stopwatch.Frequency/1000)>=cancelDeadline) return 130;
        }
      }
      uint code; Check(GetExitCodeProcess(pi.process,out code));
      return interrupted ? 130 : unchecked((int)code);
    } finally {
      // Covers assignment failure while suspended as well as normal exit.
      if(pi.process!=IntPtr.Zero) { TerminateProcess(pi.process,1); CloseHandle(pi.process); }
      if(pi.thread!=IntPtr.Zero) CloseHandle(pi.thread);
      CloseHandle(job);
    }
  }
}
'@
exit [CenterJob]::Run($Node, $Script)
