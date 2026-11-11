package agent

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// TaskName is the scheduled task that starts the sync when you log in and
// starts it again if it ever stops.
//
// A registry Run entry, which is what this used to be, starts the program
// once and never looks at it again: if it dies, the sync is quietly gone
// until the next log-in. That happened on the owner's PC on 11 Nov 2026 and
// nobody noticed for two hours. A scheduled task is the Windows way to say
// "and keep it running", which is what launchd already does on a Mac.
const TaskName = `Helium Syncer`

// taskXML describes the task. The settings that matter:
//
//   - RestartOnFailure: if the program stops without being asked to, start
//     it again a minute later. This is the whole point.
//   - DisallowStartIfOnBatteries and StopIfGoingOnBatteries are false.
//     Windows sets both to true by default, which on a laptop would stop
//     the sync the moment it is unplugged - the opposite of what a person
//     wants from a sync that is careful with the battery.
//   - ExecutionTimeLimit PT0S: no limit. It is meant to run all day.
//   - IgnoreNew: never a second copy.
//   - Hidden, and least privilege: it is not an administrator's program.
func taskXML(exe string, args []string, user string) string {
	cmd := exe
	argLine := strings.Join(args, " ")
	esc := func(s string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Keeps your bookmarks, tabs and settings the same on your computers. Starts when you log in, and starts again if it stops.</Description>
    <URI>\` + esc(TaskName) + `</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(user) + `</UserId>
    </LogonTrigger>
    <!-- The watchdog. Windows has a "restart if it fails" setting, but it
         did not fire when the program was killed on the owner's PC on
         11 Nov 2026 (the task recorded the failure and did nothing), so
         this does the job instead: every minute Windows tries to start
         it, and because two copies are never allowed, that does nothing at
         all while it is running. It only has an effect when the program is
         gone, which is exactly when it should. A minute is what a Mac
         takes too (launchd brings it back in about three seconds), and
         Windows already runs the scheduler all the time, so asking it one
         more question a minute costs almost nothing. -->
    <TimeTrigger>
      <Enabled>true</Enabled>
      <StartBoundary>2020-01-01T00:00:00</StartBoundary>
      <Repetition>
        <Interval>PT1M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(user) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <DisallowStartOnRemoteAppSession>false</DisallowStartOnRemoteAppSession>
    <UseUnifiedSchedulingEngine>true</UseUnifiedSchedulingEngine>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>99</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(cmd) + `</Command>
      <Arguments>` + esc(argLine) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// schtasks runs the Windows task tool without showing a console window.
func schtasks(args ...string) ([]byte, error) {
	cmd := exec.Command("schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("schtasks %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// writeTask creates or replaces the task. The file must be UTF-16 with a
// byte order mark, which is what schtasks reads.
func writeTask(exe string, args []string, user string) error {
	dir, err := os.MkdirTemp("", "helium-syncer-task")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "task.xml")
	if err := os.WriteFile(p, utf16BOM(taskXML(exe, args, user)), 0o600); err != nil {
		return err
	}
	_, err = schtasks("/Create", "/TN", TaskName, "/XML", p, "/F")
	return err
}

// utf16BOM turns the text into what schtasks /XML expects.
func utf16BOM(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, r := range s {
		if r > 0xFFFF {
			r = 0xFFFD
		}
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// removeTask takes the task away. A task that is not there is not an error.
func removeTask() error {
	if !taskExists() {
		return nil
	}
	_, err := schtasks("/Delete", "/TN", TaskName, "/F")
	return err
}

// taskExists reports whether the task is registered.
func taskExists() bool {
	_, err := schtasks("/Query", "/TN", TaskName)
	return err == nil
}

// runTask starts the task now. Windows starts it, not this program, so the
// new copy does not belong to whatever started this one and does not go
// away with it.
func runTask() error {
	_, err := schtasks("/Run", "/TN", TaskName)
	return err
}

// currentUser is who the task runs as.
func currentUser() string {
	if d, u := os.Getenv("USERDOMAIN"), os.Getenv("USERNAME"); d != "" && u != "" {
		return d + `\` + u
	}
	return os.Getenv("USERNAME")
}
