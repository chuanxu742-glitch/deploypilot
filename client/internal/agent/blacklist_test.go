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
	}
	for _, c := range allowed {
		if ok, reason := CheckCommand(c); ok {
			t.Errorf("expected ALLOW for %q, blocked by %q", c, reason)
		}
	}
}
