#!/usr/bin/env Rscript
# Can this business afford each plan? (the signup's suggestion)
#
# A business of 2 people may ask for everything, but that doesn't mean it can
# pay Galaxy. This estimates C, what it can put into software each month,
# and gives P(C >= what each plan would cost it), so AndRho recommends the
# biggest plan it can actually sustain instead of always the complete one.
#
# Bayesian, on a grid over log C:
#   1. Prior from the team: log C ~ Normal(log(base * k_sector * n^elasticity), sd).
#      Spending on tools grows with the team, less than proportionally
#      (elasticity < 1); the sector shifts it (manufacturing spends more
#      than a café). The defaults are assumptions, passed in by andrho-api
#      (internal/afford) so they can be tuned without touching this script.
#   2. The answer to "¿cuánto puedes invertir hoy en software al mes?" is a
#      range [lo, hi). It's the business' own word, so it dominates the
#      prior, but people misjudge it: the posterior puts probability `trust`
#      inside the range, spread evenly in quetzales (any amount of the range
#      is as likely; an open top range "Q2,990 o más" keeps the prior's
#      shape), and 1 - trust outside, following the prior and decaying with
#      the log distance to the range (Gaussian, sd `spread`): someone who
#      says "Q750 a Q1,199" may manage Q1,300, rarely Q3,000.
#   3. For each plan, its cost for this business (price + the extra users
#      it needs + the site's upkeep when it wants a site the plan lacks,
#      computed by andrho-api) -> P(C >= cost) and P(C >= 0.85 * cost)
#      ("con esfuerzo").
#   4. Recommendation: the biggest plan with P >= threshold (0.5: more
#      likely than not); if none, the cheapest one.
#
# Base R + jsonlite only. Input on stdin, output on stdout, both JSON:
#   in:  {"team": 8, "sector_factor": 1, "base": 140, "elasticity": 0.75, "sd": 0.9,
#         "budget": {"min": 750, "max": 1200} | null, "trust": 0.8, "spread": 0.35,
#         "threshold": 0.5, "plans": [{"id": "base", "cost": 120}, ...]}
#   out: {"capacity": {"median", "p10", "p90"}, "plans": [{"id", "cost", "p", "p_stretch"}],
#         "recommended", "method": "bayes_grid"}

suppressPackageStartupMessages(library(jsonlite))

input <- fromJSON(file("stdin"), simplifyVector = TRUE)
num <- function(x, default) if (is.null(x) || length(x) == 0 || is.na(x)) default else as.numeric(x)

team <- max(1, num(input$team, 1))
base <- num(input$base, 140)
elasticity <- num(input$elasticity, 0.75)
sdev <- num(input$sd, 0.9)
sector <- num(input$sector_factor, 1)
trust <- num(input$trust, 0.8)
spread <- num(input$spread, 0.35)
threshold <- num(input$threshold, 0.5)

grid <- seq(log(20), log(200000), length.out = 4000)
prior <- dnorm(grid, mean = log(base * sector * team^elasticity), sd = sdev)
prior <- prior / sum(prior)
post <- prior

b <- input$budget
if (!is.null(b) && length(b) > 0) {
  lo <- log(max(num(b$min, 0), 20))
  hi <- if (is.null(b$max) || num(b$max, 0) <= 0) Inf else log(num(b$max, 0))
  inside <- grid >= lo & grid < hi
  dist <- ifelse(grid < lo, lo - grid, ifelse(grid >= hi, grid - hi, 0))
  outside <- prior * exp(-dist^2 / (2 * spread^2)) * (!inside)
  # Even in quetzales = density proportional to C on the log grid.
  within <- if (is.finite(hi)) exp(grid) * inside else prior * inside
  p_in <- sum(within)
  p_out <- sum(outside)
  if (p_in > 0 && p_out > 0) {
    post <- ifelse(inside, within * trust / p_in, outside * (1 - trust) / p_out)
  } else if (p_in > 0) {
    post <- within
  }
  post <- post / sum(post)
}

cdf <- cumsum(post)
quant <- function(q) round(exp(grid[which(cdf >= q)[1]]))
afford <- function(cost) sum(post[grid >= log(max(cost, 1))])

plans <- input$plans
out_plans <- lapply(seq_len(nrow(plans)), function(i) {
  cost <- plans$cost[i]
  list(id = plans$id[i], cost = cost,
       p = round(afford(cost), 3), p_stretch = round(afford(0.85 * cost), 3))
})
ps <- vapply(out_plans, function(x) x$p, numeric(1))
ok <- which(ps >= threshold)
recommended <- if (length(ok) > 0) plans$id[max(ok)] else plans$id[which.min(plans$cost)]

cat(toJSON(list(
  capacity = list(median = quant(0.5), p10 = quant(0.1), p90 = quant(0.9)),
  plans = out_plans,
  recommended = recommended,
  method = "bayes_grid"
), auto_unbox = TRUE, digits = NA), "\n")
