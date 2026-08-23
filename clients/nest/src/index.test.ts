import 'reflect-metadata';
import { describe, it, expect, vi } from 'vitest';
import { Cron } from '@nestjs/schedule';
import { SCHEDULE_CRON_OPTIONS } from '@nestjs/schedule/dist/schedule.constants';
import { PulseClient, toSeconds, Pulse, configurePulse } from './index';

interface Captured {
  url: string;
  body: Record<string, unknown>;
  auth?: string;
}

function capturing(opts: { fail?: boolean } = {}) {
  const reqs: Captured[] = [];
  const fetchFn = vi.fn(async (url: string | URL, init?: RequestInit) => {
    if (opts.fail) throw new Error('connect refused');
    const headers = init!.headers as Record<string, string>;
    reqs.push({ url: String(url), body: JSON.parse(init!.body as string), auth: headers.authorization });
    return new Response(null, { status: 204 });
  }) as unknown as typeof fetch;
  const client = new PulseClient({
    baseUrl: 'https://pulse.test',
    token: 'tok',
    project: 'ops',
    fetchFn,
    now: () => 1_700_000_000_000,
    logger: () => {},
  });
  return { client, reqs };
}

describe('toSeconds', () => {
  it('parses numbers and duration strings', () => {
    expect(toSeconds(120)).toBe(120);
    expect(toSeconds('10m')).toBe(600);
    expect(toSeconds('2h')).toBe(7200);
    expect(toSeconds('1d')).toBe(86400);
    expect(toSeconds('90s')).toBe(90);
    expect(toSeconds(undefined)).toBe(0);
    expect(toSeconds('bogus')).toBe(0);
  });
});

describe('PulseClient.wrap', () => {
  it('pings start then ok with metadata', async () => {
    const { client, reqs } = capturing();
    const result = await client.wrap('job', { intervalSeconds: 300, grace: '1m' }, async () => 'done')();
    expect(result).toBe('done');
    expect(reqs.map((r) => r.body.status)).toEqual(['start', 'ok']);
    expect(reqs[1].body.project).toBe('ops');
    expect(reqs[1].body.interval_seconds).toBe(300);
    expect(reqs[1].body.grace_seconds).toBe(60);
    expect(reqs[1].body.next_expected_at).toBe(1_700_000_000 + 300);
    expect(reqs[1].auth).toBe('Bearer tok');
  });

  it('keeps the seven-field check-in contract unchanged', async () => {
    const { client, reqs } = capturing();
    await client.checkin('job', 'ok', { intervalSeconds: 60, grace: '1m', maxRuntime: '2m' }, 3);
    expect(Object.keys(reqs[0].body).sort()).toEqual([
      'duration_seconds',
      'grace_seconds',
      'interval_seconds',
      'max_runtime_seconds',
      'next_expected_at',
      'project',
      'status',
    ]);
  });

  it('bounds a hung request and aborts it with the configured timeout', async () => {
    let signal: AbortSignal | undefined;
    const logger = vi.fn();
    const fetchFn = vi.fn((_url: string | URL, init?: RequestInit) => {
      signal = init?.signal;
      return new Promise<Response>(() => {});
    }) as unknown as typeof fetch;
    const timeoutMs = 40;
    const client = new PulseClient({ baseUrl: 'https://pulse.test', fetchFn, timeoutMs, logger });

    const started = Date.now();
    await client.checkin('hung-job', 'start');
    const elapsed = Date.now() - started;

    expect(elapsed).toBeLessThan(timeoutMs * 3);
    expect(signal).toBeDefined();
    expect(signal!.aborted).toBe(true);
    expect(logger).toHaveBeenCalledWith(expect.stringContaining(`timed out after ${timeoutMs}ms`));
  });

  it('pings fail and rethrows on error', async () => {
    const { client, reqs } = capturing();
    const boom = new Error('boom');
    await expect(client.wrap('job', { intervalSeconds: 60 }, async () => { throw boom; })()).rejects.toBe(boom);
    expect(reqs[reqs.length - 1].body.status).toBe('fail');
  });

  it('is fail-open: transport errors do not affect the result', async () => {
    const { client } = capturing({ fail: true });
    const result = await client.wrap('job', { intervalSeconds: 60 }, async () => 42)();
    expect(result).toBe(42);
  });

  it('computes next_expected from a cron schedule', async () => {
    const { client, reqs } = capturing();
    await client.wrap('job', { schedule: '*/5 * * * *' }, async () => null)();
    const next = reqs[1].body.next_expected_at as number;
    expect(next).toBeGreaterThan(1_700_000_000);
    expect(next % 300).toBe(0);
  });
});

describe('register', () => {
  it('sends a register check-in with next_expected', async () => {
    const { client, reqs } = capturing();
    await client.register('job', { schedule: '0 * * * *', grace: '2m' });
    expect(reqs).toHaveLength(1);
    expect(reqs[0].body.status).toBe('register');
    expect(reqs[0].body.grace_seconds).toBe(120);
    expect(reqs[0].body.next_expected_at).toBeGreaterThan(0);
  });
});

describe('@Pulse decorator', () => {
  it('wraps a class method using the configured default client', async () => {
    const reqs: Captured[] = [];
    const fetchFn = vi.fn(async (url: string | URL, init?: RequestInit) => {
      reqs.push({ url: String(url), body: JSON.parse(init!.body as string) });
      return new Response(null, { status: 204 });
    }) as unknown as typeof fetch;
    configurePulse({ baseUrl: 'https://pulse.test', token: 't', project: 'ops', fetchFn, now: () => 1_700_000_000_000, logger: () => {} });

    class Jobs {
      ran = 0;
      @Pulse('decorated-job', { intervalSeconds: 60 })
      async run(): Promise<string> {
        this.ran += 1;
        return 'ok';
      }
    }

    const jobs = new Jobs();
    const out = await jobs.run();
    expect(out).toBe('ok');
    expect(jobs.ran).toBe(1); // `this` preserved
    expect(Jobs.prototype.run.name).toBe('run');
    expect(reqs.map((r) => r.body.status)).toEqual(['start', 'ok']);
    expect(reqs[0].url).toBe('https://pulse.test/v1/checkin/decorated-job');
  });

  it('preserves real Nest schedule metadata in either decorator order', () => {
    class Jobs {
      @Cron('*/5 * * * *')
      @Pulse('cron-first')
      async cronThenPulse(): Promise<void> {}

      @Pulse('pulse-first')
      @Cron('*/5 * * * *')
      async pulseThenCron(): Promise<void> {}
    }

    for (const name of ['cronThenPulse', 'pulseThenCron'] as const) {
      const method = Jobs.prototype[name];
      expect(method.name).toBe(name);
      expect(Reflect.getMetadata(SCHEDULE_CRON_OPTIONS, method)).toMatchObject({ cronTime: '*/5 * * * *' });
    }
  });
});
