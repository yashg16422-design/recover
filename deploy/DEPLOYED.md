# What is deployed (record of the actual AWS deployment)

Region `ap-southeast-2` (Sydney), CLI profile `recover`, every resource tagged `Project=recover`. Remove everything with `TEARDOWN.md`.

| | |
|---|---|
| Recover app (Go) + dashboard | https://3-24-252-115.sslip.io/ (Caddy, Let's Encrypt certificate, Elastic IP kept attached) |
| Fraud service (Lambda behind API Gateway) | https://ufpwfu8vpj.execute-api.ap-southeast-2.amazonaws.com (this is the app's `FRAUD_URL`) |

## What exists
- **Lambda** `python3.12`, arm64, 512 MB, loads 6 artifact files (3.35 MB) from S3 once per container; **HTTP API** throttled to 10 req/s (burst 20); log group with 7-day retention; **EventBridge** rule `rate(5 minutes)` as keep-warm. (CloudFormation stack `recover-fraud`, plus SAM's own deployment bucket/stack.)
- **S3** bucket `recover-fraud-artifacts-<account-id>`: `fraud-artifacts/` (the 6 files) and `deploy/` (the EC2 install bundle).
- **EC2** `t4g.micro`, Amazon Linux 2023 arm64, 8 GB gp3, IMDSv2 required. systemd services: `recover` (Go app on 127.0.0.1:8090), `recover-psps` (3 mock gateways on 127.0.0.1:9001-9003), `caddy` (80/443).
- **IAM** role `recover-ec2-role` (SSM managed-instance policy + read of `s3://<bucket>/deploy/*`) and profile `recover-ec2-profile`; **security group** `recover-sg`: inbound **80 and 443 only**; **key pair** `recover-key` (unused, SSH is closed).

## Security posture (as built)
- **No SSH.** Port 22 is closed; the instance is managed with SSM Run Command (SSH to a changing ISP address was unreliable, and SSM needs no open port).
- The **fraud API is public by design** (no shared secret), throttled, and serves scores for a public dataset only.
- The **dashboard is public with no login**. No SMTP/Twilio/HF credentials are on the server, so every message is `simulated`. Stripe is not connected (if you connect it, set `STRIPE_WEBHOOK_SECRET`, or unsigned events are accepted).
- State is in memory (counters, live feed reset on restart); one instance.

## Measured (nothing estimated)
| | Result | Source |
|---|---|---|
| Lambda cold start | Init 2,521 ms + first request 229 ms (about 2.75 s). Seen once, at first use. | Lambda REPORT log line |
| Lambda warm handler | p50 2.9 ms, p95 3.5 ms (141 warm invocations) | Lambda REPORT log lines |
| Keep-warm | 142 invocations logged, 1 cold start; ping clusters 5.0 min apart | CloudWatch logs + EventBridge metric |
| App to fraud lookup, full chain (EC2 -> API Gateway -> Lambda, same region) | min 15, p50 19, p95 23, max 24 ms (39 samples) | the app's own `fraud_latency_ms`; its log shows 42 fraud checks, 0 unavailable |
| From a laptop in India | each new connection adds about 0.45 s connect + 0.8 s TLS to Sydney | curl timings (network, not the server) |

**Honest caveat on cold starts:** a Lambda container can still be recycled (after a redeploy or a long quiet period). `FRAUD_TIMEOUT_MS` is 1500 ms, which is *lower* than a real cold start (about 2.75 s), so a request that lands on a cold container waits 1.5 s and then **fails open**: the dashboard shows that payment as "graph offline" and everything else works as before. The keep-warm ping makes this rare (1 cold start in 142 invocations), and the next request is warm. Raise the timeout in `/etc/recover/recover.env` if you would rather wait for the cold start.

## Verification done on the live deployment
- 21 checks against the public URL pass (UI and static files, original features identical to main, `/benchmark` byte-identical to main, graph proxy through to Lambda and S3, simulate/live/webhook, router with mock gateways, quarantine behaviour).
- The 7 web assets served are byte-identical (SHA-256) to the files tested locally.
- **Not verified in a browser on the live host**: the built-in preview pane blocked `app.js`/`app.css` with `ERR_BLOCKED_BY_CLIENT` (a client-side block; `curl` gets 200). Open the URL in your own browser to confirm.

## Operating it
Run a command on the instance (no SSH):
```bash
export AWS_PROFILE=recover AWS_REGION=ap-southeast-2
aws ssm send-command --instance-ids <instance-id> --document-name AWS-RunShellScript \
  --parameters 'commands=["systemctl is-active recover recover-psps caddy"]'
# then: aws ssm get-command-invocation --command-id <id> --instance-id <instance-id>
```
Ship a new app version: build `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/server`, rebuild the bundle (`deploy/install-on-ec2.sh` is the installer), upload it to `s3://<bucket>/deploy/recover-bundle.tgz`, run the installer through SSM, `systemctl restart recover`.
Change thresholds or the timeout: edit `/etc/recover/recover.env` (see `deploy/recover.env.example`), then `systemctl restart recover`.
Need SSH anyway: add a rule for your *current* IP to `recover-sg`, and remove it afterwards.
