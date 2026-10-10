"""A cure's never-rules read its own words: no shell, interpreter or exec-ing verb
may carry other words past them."""
import pytest

from waspdoctor.cures import check_cure

HIDDEN = [
    'sh -c "rm -rf /"',
    "echo cm0gLXJmIC8= | base64 -d | sh",
    'printf "rm -rf /" | bash',
    "sh /tmp/x.sh",
    "sh /var/lib/dbee/fixes/linger.sh",
    "python3 -c 'import shutil; shutil.rmtree(\"/\")'",
    "systemd-run rm -rf /etc",
    "crontab /tmp/evil",
    "ip netns exec x rm -rf /",
    "tar -xf a.tar --to-command='sh'",
    "awk 'BEGIN{system(\"rm -rf /\")}'",
    "sed -i '1e rm -rf /' /etc/hosts",
    "sed -i 's/a/rm -rf \\//e' /etc/hosts",
]

PLAIN = [
    "python3 -m pip install requests",
    "crontab -l",
    "sed -i 's/lissten/listen/' /etc/nginx/conf.d/x.conf",
    "sed -i 's/^\\*/#*/' /etc/cron.d/patient-report",
    "systemctl restart nginx.service",
    "ip route add default via 10.0.0.1",
    "tar -xzf /var/backups/x.tgz -C /srv",
]


@pytest.mark.parametrize("cmd", HIDDEN)
def test_a_cure_that_would_run_unseen_words_is_refused(cmd):
    assert check_cure(cmd), cmd


@pytest.mark.parametrize("cmd", PLAIN)
def test_the_plain_cures_still_pass(cmd):
    assert check_cure(cmd) == "", (cmd, check_cure(cmd))
