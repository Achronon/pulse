/**
 * pulse client + `@Pulse` decorator for NestJS / TypeScript.
 *
 * Wraps a method with heartbeat check-ins (start -> ok/fail). The next expected
 * run time is computed locally from the method's cron expression (or interval)
 * and pushed to the server, so the server needs no cron engine. Check-ins are
 * FAIL-OPEN: a monitoring error never affects the wrapped method.
 *
 * Drop-in for `@SentryCron`: keep your `@Cron(...)` and swap `@SentryCron('slug')`
 * for `@Pulse('slug', { schedule: '* * * * *' })`.
 *
 * Configure via env (PULSE_URL, PULSE_TOKEN, PULSE_PROJECT) or `configurePulse(...)`
 * at bootstrap. Requires `experimentalDecorators` (NestJS already enables it).
 */
import { parseExpression } from 'cron-parser';

/** Seconds as a number, or a string like "10m" / "2h" / "90s". */
export type DurationInput = number | string;

export interface PulseOptions {
  /** Standard 5-field cron expression; when set, next_expected_at is computed from it. */
  schedule?: string;
  /** Simple fixed period in seconds, used when schedule is absent. */
  intervalSeconds?: number;
  /** Grace after next_expected before a run is late. */
  grace?: DurationInput;
  /** Max expected runtime; enables hung-job detection. */
  maxRuntime?: DurationInput;
  /** Overrides the client's default project for this monitor. */
  project?: string;
}

export interface PulseConfig {
  baseUrl?: string;
  token?: string;
  project?: string;
  /** Injectable fetch (defaults to global fetch); handy for tests. */
  fetchFn?: typeof fetch;
  /** Injectable clock in ms epoch (defaults to Date.now); handy for tests. */
  now?: () => number;
  logger?: (message: string, error?: unknown) => void;
}

const UNITS: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86400 };

/** Coerce a number (seconds) or "10m"/"2h"/"90s" string to whole seconds. */
export function toSeconds(v: DurationInput | undefined): number {
  if (v == null) return 0;
  if (typeof v === 'number') return Math.floor(v);
  const s = v.trim();
  if (!s) return 0;
  const unit = s.slice(-1);
  if (unit in UNITS) {
    const n = Number(s.slice(0, -1));
    if (!Number.isNaN(n)) return Math.floor(n * UNITS[unit]);
  }
  const n = Number(s);
  return Number.isNaN(n) ? 0 : Math.floor(n);
}

type Status = 'register' | 'start' | 'ok' | 'fail';

export class PulseClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly project: string;
  private readonly fetchFn: typeof fetch;
  private readonly now: () => number;
  private readonly logger: (message: string, error?: unknown) => void;

  constructor(cfg: PulseConfig = {}) {
    this.baseUrl = (cfg.baseUrl ?? process.env.PULSE_URL ?? '').replace(/\/+$/, '');
    this.token = cfg.token ?? process.env.PULSE_TOKEN ?? '';
    this.project = cfg.project ?? process.env.PULSE_PROJECT ?? '';
    this.fetchFn = cfg.fetchFn ?? globalThis.fetch;
    this.now = cfg.now ?? Date.now;
    this.logger = cfg.logger ?? ((m, e) => console.warn(m, e ?? ''));
  }

  private nextExpected(o: PulseOptions): number {
    if (o.schedule) {
      try {
        const it = parseExpression(o.schedule, { currentDate: new Date(this.now()) });
        return Math.floor(it.next().getTime() / 1000);
      } catch {
        this.logger(`pulse: invalid schedule "${o.schedule}"; falling back to interval`);
      }
    }
    if (o.intervalSeconds) return Math.floor(this.now() / 1000) + o.intervalSeconds;
    return 0;
  }

  /** Send a single check-in. Never throws (fail-open). */
  async checkin(slug: string, status: Status, o: PulseOptions = {}, durationSeconds = 0): Promise<void> {
    if (!this.baseUrl) return;
    const body: Record<string, unknown> = { status };
    const project = o.project ?? this.project;
    if (project) body.project = project;
    const next = this.nextExpected(o);
    if (next) body.next_expected_at = next;
    if (o.intervalSeconds) body.interval_seconds = o.intervalSeconds;
    const grace = toSeconds(o.grace);
    if (grace) body.grace_seconds = grace;
    const maxRuntime = toSeconds(o.maxRuntime);
    if (maxRuntime) body.max_runtime_seconds = maxRuntime;
    if (durationSeconds) body.duration_seconds = durationSeconds;

    try {
      const res = await this.fetchFn(`${this.baseUrl}/v1/checkin/${slug}`, {
        method: 'POST',
        headers: {
          'content-type': 'application/json',
          ...(this.token ? { authorization: `Bearer ${this.token}` } : {}),
        },
        body: JSON.stringify(body),
      });
      if (!res.ok) this.logger(`pulse: check-in ${slug}: status ${res.status}`);
    } catch (e) {
      this.logger(`pulse: check-in ${slug} failed`, e);
    }
  }

  /** Announce a monitor at startup so a job that never fires is detectable. */
  register(slug: string, o: PulseOptions = {}): Promise<void> {
    return this.checkin(slug, 'register', o);
  }

  /** Wrap an async function with start/ok/fail check-ins; returns its result unchanged. */
  wrap<A extends unknown[], R>(slug: string, o: PulseOptions, fn: (...args: A) => Promise<R>): (...args: A) => Promise<R> {
    return async (...args: A): Promise<R> => {
      await this.checkin(slug, 'start', o);
      const started = this.now();
      try {
        const result = await fn(...args);
        await this.checkin(slug, 'ok', o, (this.now() - started) / 1000);
        return result;
      } catch (e) {
        await this.checkin(slug, 'fail', o, (this.now() - started) / 1000);
        throw e;
      }
    };
  }
}

let defaultClient: PulseClient | null = null;

/** Configure (and return) the module-level default client; call once at bootstrap. */
export function configurePulse(cfg: PulseConfig): PulseClient {
  defaultClient = new PulseClient(cfg);
  return defaultClient;
}

/** The module-level default client (lazily built from env). */
export function getPulseClient(): PulseClient {
  if (!defaultClient) defaultClient = new PulseClient();
  return defaultClient;
}

/**
 * Method decorator that wraps the method with pulse check-ins using the default
 * client. Place alongside `@Cron(...)`.
 */
export function Pulse(slug: string, options: PulseOptions = {}): MethodDecorator {
  return (_target, _propertyKey, descriptor: PropertyDescriptor): PropertyDescriptor => {
    const original = descriptor.value as (...args: unknown[]) => unknown;
    descriptor.value = function (this: unknown, ...args: unknown[]) {
      const bound = (...a: unknown[]) => Promise.resolve(original.apply(this, a));
      return getPulseClient().wrap(slug, options, bound)(...args);
    };
    return descriptor;
  };
}
