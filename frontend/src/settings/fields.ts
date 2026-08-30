/**
 * Every setting the firmware exposes, transcribed from docs/bulk-api.md.
 *
 * Keys are the firmware's own names, so a value can be traced from the camera's
 * own form to the control here without a translation table, and `form` says
 * which of the camera's two POST handlers owns the field. That is also the
 * split POST /api/settings expects in its `image` and `recording` patches.
 */

/** Which half of POST /api/settings a field belongs in. */
export type FieldForm = 'image' | 'recording'

export type FieldKind = 'bool' | 'int' | 'enum' | 'days'

export interface FieldSpec {
  key: string
  label: string
  form: FieldForm
  kind: FieldKind
  min?: number
  max?: number
  unit?: string
  options?: { value: number; label: string }[]
  hint?: string
  /** True while another setting owns this one and the camera ignores what is sent. */
  overriddenBy?: (values: Record<string, unknown>) => boolean
}

export interface FieldGroup {
  id: string
  title: string
  note?: string
  fields: FieldSpec[]
}

/**
 * The esp32-camera framesize and white balance enums. The contract calls these
 * "framesize enum" and a 0 to 4 range without naming the members, so each label
 * carries its number: if a sensor numbers them differently the value on screen
 * is still the value being sent.
 */
const FRAME_SIZES = [
  [0, '96x96'],
  [1, 'QQVGA 160x120'],
  [2, 'QCIF 176x144'],
  [3, 'HQVGA 240x176'],
  [4, '240x240'],
  [5, 'QVGA 320x240'],
  [6, 'CIF 400x296'],
  [7, 'HVGA 480x320'],
  [8, 'VGA 640x480'],
  [9, 'SVGA 800x600'],
  [10, 'XGA 1024x768'],
  [11, 'HD 1280x720'],
  [12, 'SXGA 1280x1024'],
  [13, 'UXGA 1600x1200'],
] as const

const WHITE_BALANCE = [
  [0, 'auto'],
  [1, 'sunny'],
  [2, 'cloudy'],
  [3, 'office'],
  [4, 'home'],
] as const

/** Gain ceiling doubles at every step, from 2x at 0 to 128x at 6. */
const GAIN_CEILING = [
  [0, '2x'],
  [1, '4x'],
  [2, '8x'],
  [3, '16x'],
  [4, '32x'],
  [5, '64x'],
  [6, '128x'],
] as const

function numbered(pairs: readonly (readonly [number, string])[]) {
  return pairs.map(([value, label]) => ({ value, label: `${value} - ${label}` }))
}

/** The camera owns exposure and gain itself while auto image is on. */
const autoOwnsIt = (values: Record<string, unknown>) => values.autoimg === true

export const FIELD_GROUPS: FieldGroup[] = [
  {
    id: 'image',
    title: 'Image',
    note: 'Sent to the camera as POST /image. The sensor may refuse a control, which is what the "unsupported" reading below reports.',
    fields: [
      {
        key: 'autoimg',
        label: 'Auto image',
        form: 'image',
        kind: 'bool',
        hint: 'The camera drives exposure and gain itself and ignores both fields below.',
      },
      {
        key: 'ael',
        label: 'Exposure compensation',
        form: 'image',
        kind: 'int',
        min: -2,
        max: 2,
        overriddenBy: autoOwnsIt,
      },
      {
        key: 'gc',
        label: 'Gain ceiling',
        form: 'image',
        kind: 'enum',
        options: numbered(GAIN_CEILING),
        overriddenBy: autoOwnsIt,
      },
      { key: 'bri', label: 'Brightness', form: 'image', kind: 'int', min: -2, max: 2 },
      { key: 'con', label: 'Contrast', form: 'image', kind: 'int', min: -2, max: 2 },
      { key: 'sat', label: 'Saturation', form: 'image', kind: 'int', min: -2, max: 2 },
      {
        key: 'wb',
        label: 'White balance',
        form: 'image',
        kind: 'enum',
        options: numbered(WHITE_BALANCE),
      },
      { key: 'gray', label: 'Greyscale', form: 'image', kind: 'bool' },
      { key: 'hmir', label: 'Mirror horizontally', form: 'image', kind: 'bool' },
      { key: 'vflip', label: 'Flip vertically', form: 'image', kind: 'bool' },
      {
        key: 'flashlvl',
        label: 'Flash level',
        form: 'image',
        kind: 'int',
        min: 0,
        max: 255,
      },
    ],
  },
  {
    id: 'recording',
    title: 'Recording',
    note: 'Sent to the camera as POST /recording.',
    fields: [
      { key: 'moten', label: 'Motion detection', form: 'recording', kind: 'bool' },
      {
        key: 'motsens',
        label: 'Motion sensitivity',
        form: 'recording',
        kind: 'int',
        min: 1,
        max: 100,
        unit: '% of scene',
      },
      {
        key: 'recsec',
        label: 'Recording length',
        form: 'recording',
        kind: 'int',
        min: 0,
        unit: 's',
      },
      {
        key: 'presec',
        label: 'Pre-trigger history',
        form: 'recording',
        kind: 'int',
        min: 0,
        unit: 's',
        hint: 'Seconds kept from before the trigger.',
      },
      {
        key: 'quietsec',
        label: 'Quiet before stopping',
        form: 'recording',
        kind: 'int',
        min: 0,
        unit: 's',
        hint: 'Stillness needed before a recording ends.',
      },
      {
        key: 'keepfree',
        label: 'Card space to keep free',
        form: 'recording',
        kind: 'int',
        min: 0,
        unit: 'MB',
      },
      {
        key: 'fsize',
        label: 'Frame size',
        form: 'recording',
        kind: 'enum',
        options: numbered(FRAME_SIZES),
      },
      {
        key: 'jq',
        label: 'JPEG quality',
        form: 'recording',
        kind: 'int',
        min: 0,
        max: 63,
        hint: 'Lower is better quality and a larger file.',
      },
    ],
  },
  {
    id: 'schedule',
    title: 'Schedule',
    note: 'Also part of POST /recording. Outside these hours the camera watches but does not arm.',
    fields: [
      { key: 'schen', label: 'Schedule enabled', form: 'recording', kind: 'bool' },
      { key: 'schfrom', label: 'From hour', form: 'recording', kind: 'int', min: 0, max: 23 },
      { key: 'schto', label: 'To hour', form: 'recording', kind: 'int', min: 0, max: 23 },
      {
        key: 'schdays',
        label: 'Days',
        form: 'recording',
        kind: 'days',
        hint: 'Stored as the bitmask /config calls schdays; the camera form names each day separately as schday.',
      },
    ],
  },
]

/** One distinct value and the cameras reporting it, in the order the wall lists them. */
export interface Distinct {
  value: unknown
  cameras: string[]
}

export const FIELDS_BY_KEY = new Map<string, FieldSpec>(
  FIELD_GROUPS.flatMap((g) => g.fields).map((f) => [f.key, f]),
)

/** Day 0 is bit 0. 127 is every day, which is what a camera ships with. */
export const DAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

/**
 * Settings the camera reports but neither POST handler accepts, so they are
 * shown and never edited. `unsupported` is the sensor's own complaint list.
 */
export interface ReadingSpec {
  key: string
  label: string
  /** `config` is the stored document, `record` the live poll the wall already runs. */
  source: 'config' | 'record'
  unit?: string
  hint?: string
}

export const CONFIG_READINGS: ReadingSpec[] = [
  { key: 'camname', label: 'Camera name', source: 'config' },
  { key: 'tz', label: 'Time zone', source: 'config' },
  { key: 'ssid', label: 'Wi-Fi network', source: 'config' },
  { key: 'apwin', label: 'Access point window', source: 'config' },
  {
    key: 'aelnow',
    label: 'Exposure in effect',
    source: 'config',
    hint: 'What the sensor is actually set to, which under auto image is not what is stored.',
  },
  { key: 'gcnow', label: 'Gain ceiling in effect', source: 'config' },
  {
    key: 'unsupported',
    label: 'Controls refused',
    source: 'config',
    hint: 'Empty when the sensor accepted every control it was sent.',
  },
]

export const LIVE_READINGS: ReadingSpec[] = [
  { key: 'lux', label: 'Scene brightness', source: 'record', unit: '/255' },
  { key: 'rung', label: 'Auto exposure rung', source: 'record', unit: '/10' },
  { key: 'fps', label: 'Frame rate', source: 'record', unit: 'fps' },
  { key: 'frames', label: 'Frames in clip', source: 'record' },
  { key: 'change', label: 'Scene change', source: 'record', unit: '%' },
  { key: 'threshold', label: 'Trigger threshold', source: 'record', unit: '%' },
]
