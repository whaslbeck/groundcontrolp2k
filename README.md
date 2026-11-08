# GroundControl P2K

A modern, cross-platform firmware updater for **Williams/Bally Pinball 2000**
(Revenge From Mars, Star Wars Episode I). It replaces the orphaned Windows 9x
*Pinball 2000 Update Manager*: it reads updates in every format they were
distributed in, shows their versions, checks their integrity and installs them
over RS-232.

* Single static binary for Linux, macOS and Windows. Written in Go, standard
  library only, no external dependencies.
* Interactive text menu, plus subcommands for scripting.
* Reads loose ROM files, ZIPs, self-extracting EXEs, nested archives and the
  InstallShield 3 installers (Update Manager, PUB card manager).
* Validates every file against the checksums in `bootdata.rom` exactly as the
  machine does, and completes missing files from other updates when size and
  checksum match.
* Reads the installed version from the machine and warns before a downgrade
  or a wrong game.
* Reads the installed game image back from the machine (slow, see below).
* Saves the machine's service report: audits, adjustments, high scores,
  earnings, switch counters and error logs.
* Includes an emulated machine for testing without hardware.

The protocol is documented in [docs/PROTOCOL.md](docs/PROTOCOL.md).

> **Status (version 1.0.0):**
>
> * Tested on a real machine: a Revenge From Mars with firmware 0.90 was
>   updated to 1.60 from a **Linux notebook with a real RS-232 port**.
>   `query`, `report` and `dump` were also used on real hardware.
> * macOS with a **USB-to-RS-232 adapter**: reading (`query`, `report`) works,
>   flashing did not. See "USB-to-RS-232 adapters" below.
> * Windows is only tested against the built-in emulator (under Wine), not
>   with a real machine.
> * Star Wars Episode I has not been tested (no update files available).

## Disclaimer and risks

**Use this software entirely at your own risk.** It is provided "as is",
without warranty of any kind. The author gives no guarantee that it works
and accepts no liability for any damage, loss of data, loss of settings or
high scores, or loss of the machine's function, however caused. See the
[LICENSE](LICENSE).

Writing firmware always carries a risk:

* A faulty, incomplete or wrong update image, an interrupted transfer, a
  power failure or a bad serial connection during the update can leave the
  machine's FLASH in an inconsistent state. In the worst case the **PRISM
  board can be rendered unusable** and may have to be repaired or replaced.
* An update that does not match the machine (wrong game, wrong hardware
  revision) can have the same effect.
* GroundControl checks the update files against their checksums and refuses
  to continue when they do not match, but these checks cannot rule out every
  error, in the files, on the cable or in this program.
* Settings and high scores may be lost. Save them first with `report`.

Before updating: use update files from a source you trust, check them with
`info`, make sure the machine has a stable power supply and a good cable, do
not interrupt the transfer, and do not update if you are not prepared to
accept these risks.

## Build

Programs for Linux, macOS and Windows are attached to each
[release](../../releases) on GitHub, together with a `SHA256SUMS` file; they are
built automatically from this source. To build it yourself:

```sh
go build -o groundcontrol ./cmd/groundcontrol     # Go 1.22 or newer
make dist                                         # all platforms -> dist/
go test ./...
```

## What you need

* A **null-modem cable**, DB-9 female on both ends.
* A serial port on your computer. A **real RS-232 port is preferred**, see
  below.
* The machine's serial port is inside the coin door on the left, near the
  interlock switch.
* The machine must be switched on and in **attract mode**, not in a game.
  The adjustment *COM1 mode* must be *Shell* (the default).

### USB-to-RS-232 adapters

Flashing needs a continuous 115200 baud stream with no gaps, and the machine
gives up if it gets no valid packet for about 3 seconds. Cheap USB adapters
(and their drivers) can add delays, drop bytes or switch the baud rate late.

* All successful updates so far were made with a **real RS-232 port** on a
  Linux notebook.
* With a USB adapter on macOS, reading from the machine worked, but flashing
  did not.
* If you have to use an adapter, an **FTDI-based** one is the best bet. This
  was not tested. Set its latency timer to a low value (see Troubleshooting)
  and try `-baud 57600` or `-baud 38400` if 115200 fails.
* Adapters with unknown or counterfeit chips are the usual source of trouble.

Whatever you use, start with `query`, which only reads, and keep the machine
in attract mode. A failed start writes nothing; the machine restarts and runs
the version in its PRISM ROMs until an update completes.

## Usage

Run without arguments for the menu. Updates are searched in the **current
directory**. To search elsewhere, use `-dir PATH` (can be given several times)
or put the paths at the end of the command line:

```sh
cd ~/pinball/updates && groundcontrol      # menu, updates from here
groundcontrol -dir ~/pinball/updates       # same, from anywhere
```

Commands:

```sh
groundcontrol list                                      # what was found
groundcontrol info    -set 2.60                         # details and checks
groundcontrol ports                                     # serial ports
groundcontrol query   -port /dev/ttyUSB0                # installed version
groundcontrol flash   -port COM3 -set 2.60
groundcontrol extract -set 1.80 -o rfm180
groundcontrol dump    -port COM3 -o mymachine
groundcontrol report  -port COM3                        # service report
groundcontrol linetest -port COM3                       # check the receiver, writes nothing
groundcontrol romtest -port COM3                        # graphics ROM check
groundcontrol shell   -port COM3 "game name"            # one shell command
groundcontrol version                                   # program version
groundcontrol list    -dir ~/pinball/updates -dir ~/old/rfm_180.zip
```

Options go before positional paths: `groundcontrol list -set 2 DIR`, not
`groundcontrol list DIR -set 2`. `-dir` has no such restriction.

`flash` options:

| Option | Meaning |
|---|---|
| `-set N` | update number from `list`, or a version such as `2.60` |
| `-dir PATH` | where to look for updates (default: current directory, repeatable) |
| `-mode game\|sound\|both\|auto` | `auto` sends the sound ROM too if the update has one |
| `-baud B` | 9600, 19200, 38400, 57600 or 115200 (default) |
| `-yes` | no confirmation prompts |
| `-force` | allow an update for a different game id |
| `-no-cmd` | do not type `fupdate load`, the machine already waits |
| `-dtr`, `-rts` | assert the modem lines (the original kept both off) |

`-port` also accepts `tcp:HOST:PORT` for a raw TCP serial bridge or an emulator's COM1.
Baud rate changes are not forwarded over TCP, so the remote end must not depend on them.

A complete game and sound update is about 4.7 MB and takes about 7.5 minutes
at 115200 baud.

### What happens during an update

1. The tool asks the machine for its version (`bootdata current` in the
   machine's shell, 9600 baud) and compares it with the update.
2. It types `fupdate load com1 115200 sf` and switches to 115200 baud.
3. It sends bootdata, im_flsh0, game and symbols in 512-byte packets, then
   the sound ROM. The machine shows "FLASH Update In Progress".
4. After the end packet the machine enables the new image and restarts.

If the transfer is interrupted, the machine starts the version in its PRISM
ROMs until an update completes. The update that was in the FLASH before is
gone.

### Service report

`report` collects what the machine would print on a service printer, plus a
few shell queries, and writes it to `p2k-report-<game>-<date>.txt` (or the
file given with `-o`). It takes about a minute and changes nothing on the
machine. The report contains:

* the installed version, the machine's clock and game name
* audits (plays, earnings, ball times, awards), hourly and daily earnings
* all adjustments with their current values
* high scores
* switch counters and whether the machine considers any switch broken
* power driver board version and fuse status
* the fatal and non-fatal error logs

A report taken before an update is a handy backup of the settings and high
scores. Settings cannot be written back over the shell.

### Graphics ROM test

`romtest` runs `bootdata rom verify` repeatedly (10 times by default). Each
run makes the machine add up its four graphics ROM banks on the PRISM card
and compare the sums with bootdata. The tool counts OK and BAD per bank and
explains the result:

* A bank that is **sometimes OK and sometimes BAD** is read unreliably: a
  loose ROM, a bad socket or contact, a weak data line or an unstable 5 V
  supply. A bank that is always BAD has a defective, missing or wrong ROM.
* When the machine prints the wrong sum (some firmware does), the bits in
  which it differs from the expected sum show which data lines are affected.
  If the lower bits are always right, the fault is in the upper bytes of the
  32-bit data words, i.e. in the ROM, socket or lines for that byte.
* Garbled serial output during the runs is reported too.

A failing graphics ROM check can also keep the machine from enabling a
freshly received update: it then boots from its ROMs with `[ UPDATE DISABLED ]`.
If `bootdata flash` shows all three program files as OK, `fupdate enable` and
`reboot` in a terminal program start the new version anyway.

### Diagnostic commands

These do not install anything and are meant for finding problems. All of them
accept `-trace FILE`.

| Command | Purpose |
|---|---|
| `shell -port P "COMMAND"` | runs one command in the machine's shell and prints the answer, for example `shell -port P "bootdata current"` or `shell -port P fatal`. Commands that change the machine are possible: use with care. |
| `linetest -port P [-baud B] [-set N]` | starts the update receiver and sends packets that it must reject, plain and with control characters such as XOFF (0x13) and CR (0x0d). It shows whether every byte value arrives unchanged. Nothing is written to the FLASH; the machine gives up by itself afterwards. With `-set N` the real first packet of an update is sent in rejected form. If the machine restarts, the test waits for the shell and goes on. |
| `romtest -port P [-n 10]` | the graphics ROM test described above |
| `emulate -port P [-rom FILE] [-save DIR]` | makes the computer behave like a Pinball 2000 for testing the tool, see "Testing without a machine" |

### Reading the firmware back from the machine

`dump` reads the game image from the update FLASH with the shell command
`dump`. It reads bootdata first to learn the file sizes, then the rest, and
writes `bootdata`, `im_flsh0`, `game` and `symbols` as ROM files. These are
checked against the checksums in bootdata and can be installed again with
`flash`. If update files are given as paths, the tool also reports which
release the image matches.

* It is slow. The shell runs at 9600 baud and prints a hex dump, so 1 MB takes
  about 1.5 hours and a complete RFM image takes about 6 hours.
* It reads in 4 KB chunks with a pause between them, because a large `dump`
  can stall the game long enough for its display watchdog to stop the
  machine. After such a stop the tool sends `continue` and goes on with
  smaller chunks.
* It can be interrupted with Ctrl-C. Running the same command again continues
  from `flash-dump.part` in the output directory. Use `-restart` to start over.
* `-part symbols` (or a comma separated list of `bootdata`, `im_flsh0`, `game`,
  `symbols`) reads only those files. Each is checked against the checksum the
  machine reports.
* `-addr 0x100000 -len 4096` reads any memory range into a `.bin` file, for
  example code in RAM. Interrupted reads continue here as well.
* Only the update FLASH can be read. The sound FLASH (`sf.rom`) sits behind the
  sound processor and is not reachable from the shell. A machine that has
  never received a FLASH update runs from its PRISM ROMs and has nothing to
  dump.
* Reading changes nothing on the machine.

## Troubleshooting

Start with `groundcontrol query -port …`. It only reads, and the problems
below show up there first.

**"no response from the machine"**

* The cable must be a **null modem** cable (pins 2 and 3 crossed). A straight
  extension cable does not work. To check the cable and adapter alone,
  bridge pins 2 and 3 at the far end; a terminal program then shows every
  typed character twice.
* Wrong port. `groundcontrol ports` lists the candidates. On macOS use
  `/dev/cu.*`, not `/dev/tty.*`. On Windows the number is shown in the
  Device Manager under "Ports (COM & LPT)".
* The adjustment *COM1 mode* must be *Shell*. In other modes the machine does
  not answer on the serial port.
* The machine must be fully booted into attract mode. During the power-up
  tests the shell does not answer yet.
* Another program has the port open (a terminal program, ModemManager on
  Linux). GroundControl opens the port exclusively and reports "busy".
* Linux: "permission denied" means the user is not in the `dialout` group
  (`sudo usermod -aG dialout $USER`, then log in again).
* Some adapters need DTR or RTS asserted to power their line drivers. Try
  `-dtr -rts`.

**Garbled text or "the shell did not echo the command"**

The shell runs at 9600 baud 8N1. Garbage usually means a different speed or
a bad ground connection (pin 5). A terminal program at 9600 8N1 should show
the `%` prompt after pressing Enter.

**"load FAILED" / "machine refused the update command"**

* A game is running. Wait until it is over, or type `game over` in a
  terminal program. `game info` shows `m_game_over True` when the machine is
  ready.
* The PRISM board is not detected. Then the machine shows an error at
  power-up as well.

**The transfer stops or needs many retries**

* Try a lower speed: `-baud 57600` or `-baud 38400`. Long cables and cheap
  USB adapters are the usual cause.
* Windows with FTDI adapters: set the *latency timer* in the adapter's
  advanced port settings from 16 ms to 1–4 ms.
* The machine allows only about 3 seconds per packet. If it gets no valid
  packet in that time it stops with "scheduler is hung" (shown by
  `groundcontrol shell -port … fatal`) and restarts. The tool keeps all waits
  within that limit and reports a restart.
* If the tool gives up, start the update again. The machine runs the version
  in its PRISM ROMs in the meantime; nothing is broken.

**After the update the machine still shows the old version**

Run `groundcontrol query`. If it says "PRISM ROM", the new image in the
FLASH did not pass its checks at power-up and the machine fell back to its
ROMs. Install the update again. `info -set N` shows whether the update
files themselves are complete and correct.

**"refusing to install an update for a different game"**

The machine reports a different game id than the update, for example a
Star Wars Episode I update on a Revenge From Mars. Use `-force` only if you
really mean to change the game.

**The machine typed `fupdate load` itself / the tool should not type it**

If the receiver was started by hand in a terminal, use `-no-cmd`: the tool
then sends the packets straight away.

**`dump` stops or the machine shows a fatal error**

A dump uses a lot of the machine's time. If the machine stops with a fatal
error, the tool sends `continue` and continues with smaller chunks. If the
dump breaks off, run the same command again; it continues from
`flash-dump.part`. Use `-pause 1s` to slow it down further.

**Old firmware (0.90 and similar)**

Machines with firmware from 1999 (0.90) work, but they are more sensitive
than later ones. Their receiver sometimes had one stray byte in its buffer at
the start, or garbled the first packet. The tool therefore sends one packet
that the machine must reject before the first real one, and repeats the whole
start up to five times as long as nothing has been accepted. Nothing is
written to the FLASH during failed starts, but the machine restarts each time
and needs about 30 seconds. See "Ambiguity of NAK" in
[docs/PROTOCOL.md](docs/PROTOCOL.md).

If it keeps failing, `groundcontrol linetest -port …` shows whether the
receiver gets all byte values unchanged (see "Diagnostic commands").

Before `fupdate load` the tool also asks the machine for the syntax of its
`fupdate` command and shows it. If the firmware has no sound option, it
refuses a game+sound update; install the game first with `-mode game`. Some
report sections are not available on early firmware and are marked as such.

**Still stuck**

Every command that talks to the machine accepts `-trace FILE`. It records
every byte sent and received, with timestamps and baud rate changes (data
packets as a one-line summary). Please attach that file to a bug report.

Run the same command with `-port sim` to rule out the computer side, then
connect a terminal program at 9600 8N1 and type `help` and
`bootdata current` by hand. If that works, the problem is in timing; please
report the output of the failing command.

## Testing without a machine

* `-port sim` uses a built-in emulated machine, in the menu and with all
  commands. `-sim-speed 1` makes it run at real line speed. For `dump`,
  `-sim-set N` chooses the update that the emulated FLASH holds.
* `groundcontrol emulate -port /dev/ttyUSB1 -save out/` makes a second
  computer or adapter behave like a Pinball 2000. With `socat` it also works on
  a single computer:

  ```sh
  socat pty,raw,echo=0,link=/tmp/p2k pty,raw,echo=0,link=/tmp/pc &
  groundcontrol emulate -port /tmp/p2k -save out/ &
  groundcontrol flash -port /tmp/pc -set 2.60 -yes updates/
  ```

## Supported update files

| File | Contents |
|---|---|
| `pin2000_50070_XXXX_*.exe` (1.20, 1.50, 2.50, 2.60) | PKZIP self-extractor with the ROM set |
| `pin2000_50070_XXXX_*.zip` (1.60–1.95) | ZIP containing the self-extractor |
| `rfm_180.zip`, `rfm_200.zip`, `rfm_210r4.zip` | ZIPs with ROM files. 1.80 lacks `im_flsh0.rom`, which is taken from 1.50–1.95 because it is identical. It also lacks `sf.rom`, so it updates the game only. |
| `pub_rfm_12a/14/15.exe` | InstallShield PUB card manager with ROM sets 1.20, 1.40, 1.50 |
| `Pin2000_UpMgr_130.exe` | The original Update Manager, no ROMs |
| loose `*.rom` files | `bootdata`, `im_flsh0`, `game`, `symbols`, `sf`, `pubboot` |

## Layout

| Path | Purpose |
|---|---|
| `cmd/groundcontrol` | CLI and text menu |
| `internal/fupdate` | protocol: packet plan, sender, shell queries, machine emulator, simulated cable |
| `internal/romset` | BootData, checksums, versions |
| `internal/source` | scanning directories and archives, grouping into ROM sets |
| `internal/is3` | InstallShield 3 archive reader |
| `internal/dcl` | PKWARE DCL "implode" decompressor |
| `internal/serial` | serial ports via syscalls (termios, Win32) |

## Open points

* Flashing with a USB-to-RS-232 adapter (macOS) failed. An FTDI-based adapter
  has not been tried. Reports are welcome.
* Windows has not been tried with a real machine.
* Star Wars Episode I updates should work the same way (same OS and receiver)
  but no SWE1 files were available for testing.

## License and legal notes

GroundControl P2K is released under the [MIT License](LICENSE).

* This is an independent project. It is not affiliated with, endorsed by or
  supported by Williams Electronics Games, WMS Industries, Bally, their
  successors or licensees.
* Williams, Bally, Pinball 2000, Revenge From Mars and Star Wars Episode I are
  trademarks of their respective owners. They are used here only to name the
  machines this tool works with.
* The project contains no firmware, no ROM images and no manufacturer code.
  You need your own copies of the update files. Do not add update files or
  ROM images to this repository or to its releases.
* The protocol description was created for interoperability with existing
  machines and update files; see [docs/PROTOCOL.md](docs/PROTOCOL.md).
* Updating a machine is at your own risk. See "Disclaimer and risks" above
  and the warranty disclaimer in the license.
