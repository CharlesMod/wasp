"""waspdoctor — the doctor's loop, shared by DBee and the Hive.

wake, triage (looks that only read, to a diagnosis grounded in a line a look
printed), treat (one cure at a time inside the shape, its undo recorded first,
its verify run after), and close only on a green verify. A product brings its
own patient (``run(cmd) -> Result``, ``name``, ``platform``), mind
(``chat(messages, tools) -> Reply``) and watchers (``Wake``); see protocol.py."""
