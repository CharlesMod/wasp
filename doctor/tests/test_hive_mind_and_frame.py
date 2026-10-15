"""The Hive's frame, carried by the loop so every product reaches a Hive one
way (its mind: test_a_hive_mind_calls_through_the_frames_door): a frame's command is a `hive run` job whose closing line gives
the exit."""


from waspdoctor.patient import Hive


def test_a_frames_command_is_a_job_and_its_exit_the_jobs(tmp_path):
    fake = tmp_path / "hive"
    fake.write_text("#!/bin/sh\necho \"$2 :: $3\"\necho 'frame-a: failed in 0.2s, exit 3 (job ab12)'\n")
    fake.chmod(0o755)
    r = Hive("frame-a", hive=str(fake)).run("systemctl is-active drone")
    assert r.code == 3 and r.out == "frame-a :: systemctl is-active drone"


