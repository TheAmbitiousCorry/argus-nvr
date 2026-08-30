/**
 * Deciding which camera deserves the large slot.
 *
 * The firmware reports how much of each scene changed and the percentage at
 * which that camera triggers, so "worth looking at" is a number the wall
 * already has. Turning it into a running order is the awkward part: the reading
 * arrives every couple of seconds and wobbles around the threshold, and a view
 * that swapped every time it crossed would be unwatchable. So the engine
 * remembers, between readings, what it has already decided about each camera.
 *
 * It is deliberately free of Vue and of the clock: `update` is given the time,
 * which makes the whole of it reproducible from a script.
 */

/** What the wall knows about one camera at one moment. */
export interface CameraSignal {
  id: string
  online: boolean
  /** The camera is writing a recording right now. */
  recording: boolean
  /** Percentage of the scene that changed. */
  change: number
  /** The percentage at which this camera triggers. */
  threshold: number
}

export interface FocusTuning {
  /** How long a camera keeps the focus after its motion stops. */
  dwellMs: number
  /**
   * Motion starts at the threshold but does not stop until the reading falls
   * this far below it, so a scene hovering on the line is decided once.
   */
  clearRatio: number
  /**
   * How much livelier a camera must be than the one already in the large slot
   * to take it. Without a margin, two busy scenes a couple of points apart
   * trade the slot on every poll.
   */
  challenge: number
}

export const DEFAULT_TUNING: FocusTuning = {
  dwellMs: 10_000,
  clearRatio: 0.6,
  challenge: 0.35,
}

export interface FocusRequest {
  now: number
  /** How many cameras the layout shows at full size. */
  slots: number
  /** Whether the quiet cameras should take turns in the spare slots. */
  rotate: boolean
  rotateMs: number
  /** A camera the viewer picked. It holds the first slot until released. */
  pinned: string | null
}

export interface FocusResult {
  /** Every camera, the one most worth looking at first. */
  order: string[]
  /** Cameras counted as moving, including those still inside their dwell. */
  moving: string[]
  /** Whether the timer is currently walking the quiet cameras. */
  rotating: boolean
}

interface Memory {
  /** Whether the engine currently calls this camera's scene moving. */
  hot: boolean
  /** Last moment it did, which is what the dwell counts from. */
  lastHotAt: number
}

interface Ranked {
  id: string
  /** 2 moving now, 1 inside its dwell, 0 quiet, -1 offline. */
  tier: number
  /** How hard the scene is moving, as a multiple of its own threshold. */
  score: number
  lastHotAt: number
  /** Position in the camera list, which is the order rotation follows. */
  pos: number
}

const EMPTY: FocusResult = { order: [], moving: [], rotating: false }

export class FocusEngine {
  private readonly tuning: FocusTuning
  private readonly memory = new Map<string, Memory>()
  /** The quiet camera whose turn it is, and where it sat in the camera list. */
  private cursor: string | null = null
  private cursorPos = 0
  private dueAt = 0
  /** Whether the wheel was turning last time, so a resume starts a fresh turn. */
  private spinning = false
  /** Who holds the first slot, which is what earns the right to keep it. */
  private leader: string | null = null

  constructor(tuning: Partial<FocusTuning> = {}) {
    this.tuning = { ...DEFAULT_TUNING, ...tuning }
  }

  update(signals: CameraSignal[], request: FocusRequest): FocusResult {
    if (signals.length === 0) {
      this.memory.clear()
      this.cursor = null
      this.leader = null
      return EMPTY
    }

    const { now } = request
    const ranked = signals.map((signal, pos) => this.rank(signal, pos, now))

    const live = new Set(signals.map((s) => s.id))
    for (const id of this.memory.keys()) if (!live.has(id)) this.memory.delete(id)

    const pinned = request.pinned && live.has(request.pinned) ? request.pinned : null

    const moving = ranked
      .filter((e) => e.tier >= 1)
      .sort(
        (a, b) =>
          b.tier - a.tier || b.score - a.score || b.lastHotAt - a.lastHotAt || a.pos - b.pos,
      )
    const quiet = ranked.filter((e) => e.tier === 0).sort((a, b) => a.pos - b.pos)
    const offline = ranked.filter((e) => e.tier < 0).sort((a, b) => a.pos - b.pos)

    this.hold(moving, pinned)

    const contenders = moving.filter((e) => e.id !== pinned)
    const turns = quiet.filter((e) => e.id !== pinned)
    const spare = request.slots - (pinned ? 1 : 0) - contenders.length
    const rotating = request.rotate && spare > 0 && turns.length > 1
    const wheel = this.spin(turns, rotating, request)

    const order = [
      ...(pinned ? [pinned] : []),
      ...contenders.map((e) => e.id),
      ...wheel,
      ...offline.filter((e) => e.id !== pinned).map((e) => e.id),
    ]

    this.leader = order[0] ?? null

    return {
      order,
      moving: moving.map((e) => e.id),
      rotating,
    }
  }

  private rank(signal: CameraSignal, pos: number, now: number): Ranked {
    const { clearRatio, dwellMs } = this.tuning
    const memory = this.memory.get(signal.id) ?? { hot: false, lastHotAt: 0 }
    const armed = signal.threshold > 0

    // Recording is the firmware's own verdict that something happened, so it
    // counts as motion whatever the change reading says.
    if (signal.online) {
      const over = signal.recording || (armed && signal.change >= signal.threshold)
      const under = !signal.recording && (!armed || signal.change < signal.threshold * clearRatio)
      if (over) memory.hot = true
      else if (under) memory.hot = false
      if (memory.hot) memory.lastHotAt = now
    } else {
      memory.hot = false
    }
    this.memory.set(signal.id, memory)

    const intensity = armed ? signal.change / signal.threshold : 0
    const score = Math.max(intensity, signal.recording ? 1 : 0)

    let tier = 0
    if (!signal.online) tier = -1
    else if (memory.hot) tier = 2
    else if (memory.lastHotAt > 0 && now - memory.lastHotAt < dwellMs) tier = 1

    return { id: signal.id, tier, score, lastHotAt: memory.lastHotAt, pos }
  }

  /**
   * Keep the camera already in the large slot there unless the challenger is
   * clearly livelier. Two scenes with readings a few points apart would
   * otherwise trade places on every poll, and the wall would be unwatchable
   * for exactly the reason it was worth building.
   */
  private hold(moving: Ranked[], pinned: string | null): void {
    if (pinned || moving.length < 2) return
    const leader = this.leader
    if (!leader || moving[0].id === leader) return
    const at = moving.findIndex((e) => e.id === leader)
    if (at < 1) return
    // A camera that has only just started moving outranks one that is running
    // out its dwell, however close the two readings are.
    if (moving[0].tier > moving[at].tier) return
    if (moving[0].score > moving[at].score * (1 + this.tuning.challenge)) return
    moving.unshift(...moving.splice(at, 1))
  }

  /**
   * Advance the turn among the quiet cameras. The wheel keeps its place while
   * it is not turning, whether that is because motion has the slots or because
   * the viewer paused it, so pausing holds the camera you were watching rather
   * than jumping back to the top of the list. Whichever camera has the wall
   * when the wheel starts again gets a whole interval of it rather than the
   * remains of somebody else's.
   */
  private spin(turns: Ranked[], rotating: boolean, request: FocusRequest): string[] {
    const ids = turns.map((e) => e.id)
    if (turns.length === 0) {
      this.spinning = false
      return ids
    }

    let at = turns.findIndex((e) => e.id === this.cursor)
    if (at === -1) {
      // The camera whose turn it was has gone, or has started moving and left
      // the wheel. Carry on from where it stood in the list.
      const after = turns.findIndex((e) => e.pos >= this.cursorPos)
      at = after === -1 ? 0 : after
      this.dueAt = request.now + request.rotateMs
    } else if (rotating) {
      if (!this.spinning) this.dueAt = request.now + request.rotateMs
      else if (request.now >= this.dueAt) {
        at = (at + 1) % turns.length
        this.dueAt = request.now + request.rotateMs
      }
    }

    this.spinning = rotating
    this.cursor = turns[at].id
    this.cursorPos = turns[at].pos
    return [...ids.slice(at), ...ids.slice(0, at)]
  }
}
