# Pinball 2000 serial FLASH update — protocol and file formats

This document describes how a Pinball 2000 machine receives firmware over
RS-232 and how the update files are packaged. It was written for
interoperability: to let current computers install the existing,
manufacturer-published updates on these machines. The information was
obtained by analysing the update files, the behaviour of the machine's
receiver and the behaviour of the original update tool (see section 8).

No manufacturer code or ROM content is part of this project.

## 1. Physical layer

| Item | Value |
|---|---|
| Machine port | COM1 of the CPU board. The DB-9 connector is inside the coin door, on the left near the interlock switch. |
| Cable | Null modem, pins 2 and 3 crossed, DB-9 female on both ends |
| Line format | 8 data bits, no parity, 1 stop bit, no flow control |
| Modem lines | The original tool kept DTR and RTS off; GroundControl does the same by default |
| Shell | The machine's command shell runs on COM1 at 9600 baud (adjustment *COM1 mode = Shell*) |

## 2. Starting the receiver

The receiver is started with a shell command:

```
fupdate load <com1 | com2> <baudrate> [sf | sfonly]
```

| Last argument | Transfer content |
|---|---|
| none | game image |
| `sfonly` | sound FLASH only |
| `sf` | game image, then sound FLASH |

Accepted baud rates: 9600, 19200, 38400, 57600, 115200.

Sequence used by the PC:

1. Open the port at 9600 baud and discard pending input.
2. Send the command followed by a line feed. The original tool always names
   `com1`.
3. Wait until the command has been transmitted, then switch the PC port to
   the transfer baud rate and discard input again.
4. Send the packets.

The machine accepts the command only while no game is running and the PRISM
board is present. Otherwise the shell answers `load FAILED`. When accepted,
the machine stops the shell on that port, switches the port to the requested
speed, shows a "FLASH Update In Progress" screen with a progress bar and waits
for packets.

## 3. Packets

### Data packet, PC to machine (always 518 bytes)

| Offset | Size | Field |
|---|---|---|
| 0 | 2 | block number, or a marker value (see below) |
| 2 | 2 | number of valid payload bytes, at most 512 |
| 4 | 2 | checksum: the 16-bit sum of the valid payload bytes, negated |
| 6 | 512 | payload, unused bytes set to zero |

### Reply, machine to PC (4 bytes)

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | 6 = ACK, 21 = NAK |
| 1 | 1 | padding, undefined value |
| 2 | 2 | block number copied from the received packet |

All multi-byte values are little endian. There is no start-of-packet marker;
the receiver frames packets purely by counting 518 bytes.

### Marker packets

Both have length 0.

| Block | Meaning |
|---|---|
| 0xFFFE | Start of sound data. The following payload goes to the sound FLASH, starting at offset 0. Rejected if the command had no `sf`/`sfonly`. |
| 0xFFFF | End of transfer. |

### Receiver behaviour

* Before receiving, the receiver of releases 1.20 to 2.60 sets the port to
  8N1 at the requested speed and switches its terminal driver to raw mode.
  In the shell's line mode the driver handles special characters (XON/XOFF
  flow control, CR to LF, line editing). Those would corrupt the binary
  packets. A receiver that skipped this step could not receive updates.

* It waits until 518 bytes are buffered. If that takes longer than about
  5 seconds, it discards the buffered bytes and waits again. After the sixth
  such timeout for the same packet the update fails.
* **Time limit per packet.** While waiting, the receiver polls in a loop and
  does not give up the processor, and it has preemption switched off. Before
  each packet it resets the machine's scheduler hang detector. If no packet is
  accepted within about 3 seconds (3000 ticks of a 1 ms counter), the machine
  stops with the fatal error `interval_0_25ms: scheduler is hung` and restarts.
  This happens before the 5 second input timeout can take effect, so in
  practice a stalled or misaligned transfer cannot be resynchronised by
  waiting: every packet, including its repetitions, must be accepted within
  about 3 seconds of the previous one. Observed on a machine with 0.90; the
  receiver code is the same in all later versions.
* A packet is accepted (ACK) when its length is at most 512, the checksum is
  correct and its block number is the expected one, or is a marker value.
  Every other packet is answered with NAK and is expected again.
* Block numbers count the data packets from 0, across all files and across
  the sound marker. A marker does not advance the counter.
* Accepted payload is written to the update FLASH at a running offset. A
  payload length that is not a multiple of 4 is rounded up; this is
  tolerated once. The image may not exceed 4 MB.
* After the end marker the machine finishes the progress screen, marks the
  new image as enabled (unless only sound was sent) and restarts.

### Ambiguity of NAK

A NAK does not tell whether the packet was damaged or was a duplicate of one
that had already been accepted, because the receiver NAKs both. A duplicate
happens when the PC resends a packet whose ACK got lost. The original tool
accepted a NAK with a matching block number as success. That recovers from a
lost ACK, but silently skips a packet that was really damaged.

GroundControl resolves it like this:

* NAK on a packet whose previous attempt got a clear answer: resend it.
* NAK after an attempt whose reply was lost: send the next packet as a probe.
  The machine ACKs the probe only if it already has the previous packet.
  If the probe is NAKed, the previous packet is resent.
* Reply timeouts are chosen to fit the time limit per packet: the time the
  packet takes on the line plus 1 second (plus 1.5 seconds for the first
  packet). Repetitions are sent immediately.
* Before the first real packet the tool sends one warm-up packet that the
  machine must reject (block number 0xFFFD, wrong checksum, plain bytes). On a
  machine with 0.90 the first packet after the start arrived garbled when it
  contained the byte 0x13; the same packet sent second arrived intact. The
  warm-up packet costs one packet time and is harmless on every version.
* On a machine with 0.90, some starts of the receiver had one stray byte in
  its buffer before the first packet, so every packet was framed one byte off
  and rejected until the hang detector restarted the machine. Nothing is
  written in that case. GroundControl then waits for the shell and starts
  again, up to five times, as long as no packet has been accepted yet.
* If the machine restarts anyway (for example after a byte was lost on the
  line, which shifts the framing), the tool recognises the start-up messages
  and stops with a clear message. The machine then runs its previous version
  from the PRISM ROMs or, if bootdata was not yet overwritten, from FLASH.
* The end marker is resent until it is acknowledged, at most three times.
* After the start command the PC listens at 9600 baud for 0.5 seconds and
  aborts if the shell reports an error.

## 4. Content of a transfer

The game image consists of four files, sent in this order. Each is sent with
exactly the size recorded in bootdata. Every file is split into 512-byte
packets, and its last packet is shorter if needed, so the files lie
back-to-back in the FLASH.

| Order | File | Size | FLASH offset |
|---|---|---|---|
| 1 | `bootdata.rom` | 32768 bytes | 0x000000 |
| 2 | `im_flsh0.rom` | from bootdata | 0x008000 |
| 3 | `game.rom` | from bootdata | directly after im_flsh0 |
| 4 | `symbols.rom` | from bootdata | directly after game |

The bootdata copy is sent with its enable flag cleared. The machine sets the
flag itself after a successful transfer, so an incomplete image is never
used. For `sf` and `sfonly` the sound marker and `sf.rom` follow, which is
always exactly 1 MiB.

`pubboot.rom` is not sent. It belongs to the PRISM Update Board, an ISA card
that was an alternative update method without a PC.

At power-up the machine uses the FLASH image only if its bootdata is valid,
enabled and the image checksums match. Otherwise it runs the game version
held in the PRISM board ROMs. An interrupted transfer therefore leaves a
working machine running the ROM version. The FLASH image that was installed
before is lost, because bootdata is overwritten by the first packet.

## 5. bootdata.rom

32768 bytes. All values are little-endian 32-bit words unless noted. Sizes
are counted in 32-bit words.

| Offset | Content |
|---|---|
| 0x00 | build timestamp, text, 32 bytes, e.g. `Thu Aug 08 12:44:50 2024` |
| 0x20 | structure version, two 16-bit values (1, 1 in all known files) |
| 0x24 | structure size in words, always 0x2000 |
| 0x28 | correction value that makes the bootdata checksum zero |
| 0x2C | size of im_flsh0.rom |
| 0x30 | checksum of im_flsh0.rom |
| 0x34 | system version, major part, two ASCII characters such as `1 ` |
| 0x38 | system version, minor part, two ASCII characters such as `37` |
| 0x3C | game id: 50069 Star Wars Episode I, 50070 Revenge From Mars |
| 0x40 | game version, major |
| 0x44 | game version, minor |
| 0x48 | load address of game.rom, 0x100000 |
| 0x4C | size of game.rom |
| 0x50 | checksum of game.rom |
| 0x54 | size of symbols.rom |
| 0x58 | checksum of symbols.rom |
| 0x5C–0x7B | size and checksum of the four PRISM ROM banks |
| 0x7C | identification text `13Dec96`, 8 bytes |
| 0x7D00 | enable flag: 0 = image disabled, 1 = enabled |

Rules:

* A checksum is the 32-bit sum of all little-endian words of a file. This
  holds for every file of every update examined.
* Bootdata is valid if the structure version is not 0, the size is 0x2000,
  the identification text is present and the sum of the first 8000 words is
  0. The enable flag lies after these 8000 words and is not covered.
* The machine prints the system version as two ASCII digits per part,
  `1.37`. If the structure version were 1.0, the fields would be plain
  numbers.
* The machine prints the game version as two integers: release 1.80 appears
  as `1.8`, release 2.60 as `2.60`. Releases 1.90, 1.91 and 1.95 all carry
  1.9. GroundControl therefore takes the release number from the file name
  (`pin2000_<game id>_<MMmm>_…`).
* `symbols.rom` is a symbol table, starting with `SYMBOL TABLE`. GroundControl
  uses it to find the operating system version text inside `game.rom`.

## 6. Shell commands around an update

| Command | Purpose |
|---|---|
| `bootdata current` | shows the bootdata the machine runs from, introduced by `PRISM ROM:` or `PRISM FLASH:`, one `name: value` line per field, then the result of the image checks |
| `bootdata rom`, `bootdata flash` | shows a specific copy |
| `fupdate enable`, `fupdate disable` | sets or clears the enable flag of the FLASH image |
| `reboot` | restarts the machine |
| `dump <address> [<count>]` | prints memory as a hex dump, default 256 bytes |
| `printout <audits \| adjustments \| hstd \| hourly \| daily \| pricing \| fatal \| nonfatal \| everything>` | the service printer report as text, each part headed by game number, game and OS version and the machine's clock |
| `hstd` | all high score tables with their members |
| `game name` | manufacturer, title, game id, version and build date in one line |
| `game info` | game state, including `m_game_over` |
| `game over` | ends a game in progress |
| `switch counters` | switch maintenance counters and the list of switches considered broken |
| `pdb` | power driver board version and fuse status |
| `time` | the machine's clock |
| `fatal`, `nonfatal` | the error logs |
| `help` | lists all commands |

The shell runs commands one at a time and echoes the typed line. Output
ends with the prompt `% `. Unknown commands are answered with
`<name>: not found`. Lines end with CR LF. Between print buffers the
output can pause for a fraction of a second, so the end of a command's
output is best detected by the prompt or by a silence of more than a
second.

### Reading memory with `dump`

The update FLASH is mapped into the address space at 0x12000000, so `dump`
can read the installed image. The output begins with a title line `dump:`,
followed by one line per 16 bytes:

```
0x12000000  53 75 6e 20 41 70 72 20 32 33 20 30 39 3a 34 30    Sun Apr 23 09:40
```

The line holds the address as `0x` and eight hex digits, then the bytes as
two-digit hex numbers separated by spaces, then the bytes as text with
non-printable characters shown as `.`. The last line can be shorter. The
shell prompt `% ` follows.

* At 9600 baud a line of about 82 characters carries 16 bytes, which is about
  9 minutes per 100 KB.
* While a dump is printed, the rest of the game gets less processor time. In
  an emulator, a single dump of 64 KB made the display watchdog stop the
  machine with `*** Fatal: … Render pass watchdog has expired`. The machine
  then waits in a monitor (`monitor commands: <?> <continue> <reboot>`).
  Chunks of 4 KB with short pauses did not cause this.
* The text column can contain any characters, including the prompt
  characters or words like "Fatal". Messages must only be looked for in lines
  that are not dump lines.
* The sound FLASH is behind the sound processor and is not mapped. The
  firmware contains other read commands (`pci flash read`, `pci snd read`),
  but they are not available in the shell of released games.

## 7. Distribution formats

| Format | Examples | Notes |
|---|---|---|
| ROM files | `pin2000_50070_0260_game.rom` | file names `<prefix>` + `bootdata`, `im_flsh0`, `game`, `symbols`, `sf`, `pubboot` + `.rom` |
| ZIP | `rfm_180.zip`, `rfm_210r4.zip` | can mix versions: `rfm_200.zip` contains the 1.91 `sf.rom` |
| PKZIP self-extracting EXE | `pin2000_50070_0120_…_B_10000000.exe` | a ZIP behind a small program stub |
| ZIP containing such an EXE | `pin2000_50070_0160_…zip` | nested |
| InstallShield 3 installer | `Pin2000_UpMgr_130.exe`, `pub_rfm_12a/14/15.exe` | see below |

**InstallShield 3.** Archives are appended to a small program. Each starts
with the bytes `13 5D 65 8C 3A 01`.

| Header offset | Content |
|---|---|
| 0x0C | number of files, 16 bit |
| 0x0E | DOS date and time |
| 0x1E | number of volumes |
| 0x1F | number of this volume |
| 0x29 | offset of the directory table |
| 0x31 | number of directories |
| 0x33 | offset of the file table |

| File entry offset | Content |
|---|---|
| 0 | last volume holding the file |
| 1 | directory index |
| 3 | uncompressed size |
| 7 | compressed size |
| 11 | data offset in the first volume |
| 15 | DOS date and time |
| 23 | length of this entry |
| 28 | first volume holding the file |
| 29 | length of the name, followed by the name |

A file spanning several volumes continues at offset 0xFF of the next volume.
The data is compressed with PKWARE DCL "implode". All files seen use binary
mode with a 4 KB dictionary.

The `pub_rfm_*` installers are the PUB card programmer for Windows 95 and
contain the complete ROM sets 1.20, 1.40 and 1.50. `Pin2000_UpMgr_130.exe` is
the original update tool and contains no ROMs.

## 8. How this was determined

* Update files: their structure, checksums and the bootdata layout were
  derived by comparing all available releases.
* Machine side: the receiver code in the game firmware was analysed to learn
  packet validation, timeouts and the reaction to markers. This analysis
  served solely to make an independent program interoperate with the
  machine, as permitted by § 69e of the German Copyright Act and Article 6 of
  Directive 2009/24/EC.
* Original tool: the original update tool and the files distributed with it
  were analysed for the same purpose, to learn how the PC side behaves.
  GroundControl is an independent implementation and contains no code from
  it.
* GroundControl's emulator of the machine side was written from this
  description and is used to test the sender.
