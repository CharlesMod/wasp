"""Hive looks: the doctor reads the Hive through its own read verbs, and nothing
that writes, launches a job or never ends."""
import pytest

from waspdoctor.looks import check


@pytest.mark.parametrize("cmd", [
    "hive", "hive fleet", "hive journal rocky --since 2h", "hive journal all --budget 4000 --json",
    "hive doctor", "hive doctor case 2026-10-08-lapses-1", "hive needs show", "hive needs --json",
    "hive deploy status", "hive queen show", "hive job logs 06GJ62VZ4SG29C8DJZZENJGCYR | tail -40",
    "hive roles", "~/hive/bin/hive fleet | grep pop-os",
    "curl -s http://court.example:4410/v1/events?tail=20",
])
def test_the_hives_reads_are_looks(cmd):
    assert check(cmd) == ""


@pytest.mark.parametrize("cmd,why", [
    ("hive serve cube qwen3.5-4b-iq4xs --standing", "not one of the Hive's reads"),
    ("hive run queen df -h", "not one of the Hive's reads"),
    ("hive roles tag cube queen", "acts"),
    ("hive needs set messenger --models x", "acts"),
    ("hive doctor hint 2026-10-08-lapses-1 x", "acts"),
    ("hive doctor probe engine", "acts"),
    ("hive job cancel 06GJ62VZ4SG29C8DJZZENJGCYR", "acts"),
    ("hive deploy queen", "acts"),
    ("hive todo next --claim", "acts"),
    ("hive journal rocky --follow", "never does"),
    ("curl -X POST http://court.example:4410/v1/drones/cube/release", "writes"),
])
def test_what_acts_on_the_hive_is_not_a_look(cmd, why):
    assert why in check(cmd)
