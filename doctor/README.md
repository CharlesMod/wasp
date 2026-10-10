# waspdoctor

The doctor's loop, shared by DBee and the Hive's doctor bee: one implementation
in the kit both build on, so neither depends on the other.

A case is a wake, a triage of looks that only read (`looks`), a diagnosis that
must quote a line a look printed, then one cure at a time inside the shape
(`cures`), its undo recorded before it runs and its verify run after, and a
close only on a green verify. On a Hive frame the doctor may also hold the frame
on its court while it mends it, and release an action the court quarantined
there (`court`). Every case is on disk after every turn, with what the mind saw
and said, for training.

A product brings its own patient, mind and watchers in the shapes of
`protocol.py`, and may bring its own word on its own wakes: `reread(wake)` says
whether what woke the doctor still reads red, `vet(command, undo)` refuses a
written cure by the product's own laws. A Hive is reached one way by every
product: a frame is `patient.Hive` (each command a `hive run` job), its mind
`minds.HiveMind` (a seat from the router). DBee is one product
(github.com/CharlesMod/dbee); the Hive's doctor bee is another.

    python3 -m pytest -q        # from this directory
