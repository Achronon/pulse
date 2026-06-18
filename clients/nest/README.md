# @achrononlimited/pulse-nest

pulse heartbeat client + `@Pulse` decorator for NestJS / TypeScript. Fail-open: a
monitoring error never affects your job.

Drop-in for `@SentryCron` — keep your `@Cron(...)` and swap the decorator:

```ts
import { Cron } from '@nestjs/schedule';
import { Pulse } from '@achrononlimited/pulse-nest';

@Cron('*/5 * * * *')
@Pulse('empera-booking-expiry', { schedule: '*/5 * * * *', grace: '2m', maxRuntime: '4m' })
async expireStaleBookings() {
  // ...
}
```

Configure once at bootstrap (or rely on env `PULSE_URL`, `PULSE_TOKEN`, `PULSE_PROJECT`):

```ts
import { configurePulse } from '@achrononlimited/pulse-nest';

configurePulse({
  baseUrl: process.env.PULSE_URL,
  token: process.env.PULSE_TOKEN,
  project: 'empera',
});
```

`@Pulse` pings `start`, runs the method, then `ok` (with duration) or `fail`. The next
expected run time is computed locally from `schedule` (or `intervalSeconds`) and pushed to
the server — the server does no cron parsing. Durations accept seconds (`240`) or strings
(`'4m'`).

Announce a monitor at startup so a job that never fires is detectable:

```ts
import { getPulseClient } from '@achrononlimited/pulse-nest';
await getPulseClient().register('empera-booking-expiry', { schedule: '*/5 * * * *', grace: '2m' });
```

Requires `experimentalDecorators` (NestJS enables it by default).
