# Copy this file to env.sh, fill in your values, then run:
#     source env.sh && make run
# env.sh is gitignored — never commit real secrets.

# --- AI messages (optional; without it you get solid templates) ---
export HF_TOKEN=hf_xxxxxxxx
export HF_MODEL=Qwen/Qwen2.5-72B-Instruct      # optional; swap if this model isn't served for you

# --- Email sending (needed for REAL emails; Gmail example) ---
export SMTP_HOST=smtp.gmail.com
export SMTP_PORT=587                            # optional (defaults to 587)
export SMTP_USER=you@gmail.com
export SMTP_PASS=your16charapppassword          # Google Account > Security > App passwords
export SMTP_FROM=you@gmail.com

# --- Message branding (optional) ---
export MERCHANT_NAME="Chai & Co"
export MERCHANT_PAY_LINK=https://yourshop.example/pay/

# --- Live Stripe webhook (optional; from `stripe listen` or the Stripe dashboard) ---
export STRIPE_WEBHOOK_SECRET=whsec_xxxxxxxx

# --- Autonomous mode (optional): auto-recover every incoming webhook failure ---
# export AUTO_RECOVER=true
# export TEST_RECIPIENT=you@gmail.com           # where auto-recovered messages go

# --- SMS via Twilio (optional; NOTE: trial accounts block custom SMS templates) ---
# export TWILIO_ACCOUNT_SID=ACxxxxxxxx
# export TWILIO_AUTH_TOKEN=xxxxxxxx
# export TWILIO_FROM=+1xxxxxxxxxx
