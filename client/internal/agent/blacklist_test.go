package agent

import "testing"

func TestCheckCommandBlocks(t *testing.T) {
	blocked := []string{
		"rm -rf /",
		"rm -rf /*",
		"sudo rm -rf / --no-preserve-root",
		"rm -fr ~",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda bs=1M",
		"echo x > /dev/sda",
		"wipefs -a /dev/nvme0n1",
		":(){ :|:& };:",
		"shutdown -h now",
		"reboot",
		"sudo poweroff",
		"sudo -n reboot",
		"apt-get update && reboot",
		"echo done; shutdown -r +1",
		"/sbin/shutdown now",
		"systemctl reboot",
		"sudo systemctl poweroff",
		"init 0",
		"chmod -R 777 /",
		"chown -R nobody /",
		// Quoted root and Xen/AWS disks.
		`rm -rf "/"`,
		`sudo rm -rf '/*'`,
		"dd if=/dev/zero of=/dev/xvda bs=1M",
		// Windows.
		"Format-Volume -DriveLetter D",
		"Get-Disk 1 | Clear-Disk -RemoveData",
		"diskpart /s wipe.txt",
		"format C: /q",
		"Remove-Item -Recurse -Force C:\\",
		`Remove-Item C:\Windows -Recurse -Force`,
		"rd /s /q C:\\",
		"Remove-Item -Recurse $env:SystemRoot",
		"Stop-Computer -Force",
		"Restart-Computer",
		"shutdown /r /t 0",
		"bcdedit /delete {current}",
		"vssadmin delete shadows /all /quiet",
		// Gaps found by the deep agent test: nested shells, Start-Process,
		// wildcard and forward-slash roots, the profile, find and sysrq.
		`bash -c "reboot"`,
		`sh -c 'shutdown -h now'`,
		`bash -lc "rm -rf /"`,
		`powershell -c "Stop-Computer -Force"`,
		`pwsh -NoProfile -Command "Restart-Computer"`,
		`cmd /c "shutdown /s /t 0"`,
		`powershell -c "cmd /c 'shutdown /r /t 0'"`,
		`Start-Process shutdown -ArgumentList '/s','/t','0'`,
		`Start-Process -FilePath "shutdown.exe" -ArgumentList "/r"`,
		`Remove-Item C:\* -Recurse -Force`,
		`rm -r -fo C:/`,
		`Remove-Item -Recurse -Force D:\`,
		`Remove-Item -Recurse -Force $env:USERPROFILE`,
		`Remove-Item -Recurse -Force ~`,
		`find / -delete`,
		`find / -type f -delete`,
		`echo b > /proc/sysrq-trigger`,
	}
	for _, c := range blocked {
		if ok, _ := CheckCommand(c); !ok {
			t.Errorf("expected BLOCK for %q", c)
		}
	}
}

func TestCheckCommandAllows(t *testing.T) {
	allowed := []string{
		"rm -rf ./build",
		"rm -rf /tmp/deploy-cache",
		"rm -rf node_modules",
		"pip install vllm",
		"docker run --gpus all -p 8000:8000 myimg",
		"modelscope download --model qwen/Qwen2.5-0.5B --local_dir ./models/qwen",
		"curl -fsSL https://ollama.com/install.sh | sh",
		"dd if=/dev/zero of=./testfile bs=1M count=10",
		"chmod -R 755 /opt/app",
		"systemctl restart myservice",
		// Read-only commands that only mention power words (real false
		// positives seen in testing).
		`ps aux | grep -iE "deploypilot|sshd|systemd" | grep -v grep; last -x | head -20; ls /var/log/ | grep -iE "shutdown|syslog|auth"`,
		`journalctl -b -1 | grep -i 'reboot'`,
		"last -x reboot",
		"last reboot | head",
		"echo 'do not reboot'",
		"cat /var/log/shutdown.log",
		"systemctl status reboot.target",
		// Windows commands that must keep working.
		`Remove-Item -Recurse -Force .\build`,
		`Remove-Item -Recurse -Force C:\temp\cache`,
		`rd /s /q C:\Users\me\AppData\Local\Temp\x`,
		"Get-Disk | Format-Table",
		"Get-Volume",
		"Get-Help Restart-Service",
		`rm -rf "./dist"`,
		// Near misses for the nested-shell, profile and find rules.
		`bash -c "echo will reboot later"`,
		`bash -c "grep -E 'shutdown|reboot' /var/log/syslog"`,
		`sh -c "cd /opt/app && ./start.sh"`,
		`powershell -Command "Get-Content C:\logs\app.log -Tail 50"`,
		`cmd /c "dir C:\"`,
		`cmd /c "rd /s /q C:\temp\build"`,
		`Remove-Item -Recurse -Force $env:USERPROFILE\.cache\pip`,
		`Remove-Item -Recurse -Force ~\Downloads\old`,
		`Remove-Item C:\temp\* -Recurse -Force`,
		`Start-Process python -ArgumentList 'app.py' -PassThru`,
		`Get-Process | Where-Object Name -eq shutdown`,
		`find /tmp/build -delete`,
		`find . -name '*.pyc' -delete`,
		`find /var/log -name '*.gz' -mtime +7 -delete`,
		`cat /proc/sysrq-trigger`,
	}
	for _, c := range allowed {
		if ok, reason := CheckCommand(c); ok {
			t.Errorf("expected ALLOW for %q, blocked by %q", c, reason)
		}
	}
}

func TestCheckPowerShellProtectsTheSharedTerminal(t *testing.T) {
	blocked := []string{
		// The command a live run produced.
		"$pid = (Get-NetTCPConnection -LocalPort 54026).OwningProcess\nif ($pid) { Stop-Process -Id $pid -Force }",
		"$PID=1234",
		"foreach ($pid in $ids) { Stop-Process -Id $pid }",
		"Stop-Process -Id $PID",
		"taskkill /F /PID $pid",
	}
	for _, c := range blocked {
		if ok, _ := CheckPowerShell(c); !ok {
			t.Errorf("expected BLOCK for %q", c)
		}
	}
	allowed := []string{
		"$procId = (Get-NetTCPConnection -LocalPort 8080).OwningProcess; Stop-Process -Id $procId",
		`"shell is $PID"`,
		"Write-Output $PID",
		"if ($PID -eq 4) { 'x' }",
		"$pidFile = 'app.pid'; Get-Content $pidFile",
		"$appPid = 5; Stop-Process -Id $appPid",
		`Write-Host 'use $pid = 1 in bash'`,
	}
	for _, c := range allowed {
		if ok, reason := CheckPowerShell(c); ok {
			t.Errorf("expected ALLOW for %q, blocked by %q", c, reason)
		}
	}
}
