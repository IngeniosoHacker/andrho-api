#!/usr/bin/env Rscript
# Multifactor ANOVA of visit engagement for the Web section's suggestions.
#
# Question it answers: which factors of a visit (where it came from, device,
# landing page, weekday vs weekend, country) actually change how much people
# engage -- and which levels are significantly better or worse -- instead of
# eyeballing averages that differ by chance.
#
# For each outcome (time on site, pages per visit, scroll depth):
#   1. levels with fewer than `min_level_n` visits are pooled into "otras";
#      factors left with < 2 levels are dropped;
#   2. a linear model y ~ main effects + the two-way interactions whose cells
#      are populated enough (source:device, landing:device);
#   3. Type II sums of squares (each term adjusted for every term that doesn't
#      contain it -- right for unbalanced web data), F tests, partial eta^2;
#   4. Tukey HSD between levels of each significant factor;
#   5. assumption checks: Brown-Forsythe (Levene, median-centred) per factor
#      and residual skewness; a Kruskal-Wallis test per factor as a
#      distribution-free cross-check, so a "significant" effect that only
#      exists because of outliers can be flagged as not robust.
# Time and pages are skewed, so they're modelled on log1p and reported back
# on their own scale (geometric means and ratios).
#
# Base R + jsonlite only (installable as r-base-core + r-cran-jsonlite).
# Input on stdin, output on stdout, both JSON:
#   in:  {"analysis": "todas", "alpha": 0.05, "min_level_n": 20,
#         "factors": ["source","device","landing","daytype","country"],
#         "outcomes": ["duration_s","pages","scroll"],
#         "data": {"source": [...], "device": [...], ..., "duration_s": [...]}}
#   out: {"analysis", "n", "models": [ { "outcome", "transform", "n", "r2",
#         "effects": [...], "levels": {...}, "posthoc": {...},
#         "assumptions": {...}, "robust": {...}, "cells": [...] } ], "warnings"}

suppressPackageStartupMessages(library(jsonlite))

input <- fromJSON(file("stdin"), simplifyVector = TRUE)
alpha <- if (is.null(input$alpha)) 0.05 else input$alpha
min_n <- if (is.null(input$min_level_n)) 20 else input$min_level_n
factors <- input$factors
outcomes <- input$outcomes
df <- as.data.frame(input$data, stringsAsFactors = FALSE)
warnings_out <- character(0)

TRANSFORM <- list(duration_s = "log1p", pages = "log1p", scroll = "none")

pool_levels <- function(x, min_n) {
  x[is.na(x) | x == ""] <- "desconocido"
  tab <- table(x)
  rare <- names(tab)[tab < min_n]
  x[x %in% rare] <- "otras"
  # "otras" itself may still be too small: fold it into the biggest level's
  # neighbourhood by leaving it; it's dropped below if < min_n.
  f <- factor(x)
  keep <- names(table(f))[table(f) >= min_n]
  f <- factor(ifelse(f %in% keep, as.character(f), NA))
  # Most frequent level first = the reference level, so contrasts read
  # "compared with the usual visit".
  if (nlevels(f) > 0) f <- relevel(f, ref = names(sort(table(f), decreasing = TRUE))[1])
  f
}

rss <- function(fit) sum(residuals(fit)^2)

# Type II: SS(term | all terms not containing it).
type2 <- function(y, terms, data) {
  full <- lm(reformulate(terms, response = y), data = data)
  res_df <- df.residual(full)
  mse <- rss(full) / res_df
  out <- lapply(terms, function(t) {
    t_vars <- strsplit(t, ":", fixed = TRUE)[[1]]
    contains <- vapply(terms, function(u) {
      u_vars <- strsplit(u, ":", fixed = TRUE)[[1]]
      all(t_vars %in% u_vars)
    }, logical(1))
    others <- terms[!contains]
    without <- if (length(others)) reformulate(others, response = y) else as.formula(paste(y, "~ 1"))
    with <- if (length(others)) reformulate(c(others, t), response = y) else reformulate(t, response = y)
    m0 <- lm(without, data = data)
    m1 <- lm(with, data = data)
    ss <- rss(m0) - rss(m1)
    dft <- df.residual(m0) - df.residual(m1)
    if (dft <= 0) return(NULL)
    f <- (ss / dft) / mse
    list(term = t, df = dft, ss = ss, F = f,
         p = pf(f, dft, res_df, lower.tail = FALSE),
         partial_eta2 = ss / (ss + rss(full)))
  })
  list(fit = full, effects = Filter(Negate(is.null), out), res_df = res_df)
}

# Brown-Forsythe: ANOVA on |y - median(group)|.
brown_forsythe <- function(y, g) {
  ok <- !is.na(g)
  y <- y[ok]; g <- droplevels(g[ok])
  if (nlevels(g) < 2) return(NA)
  d <- abs(y - ave(y, g, FUN = median))
  s <- summary(aov(d ~ g))[[1]]
  s[["Pr(>F)"]][1]
}

skewness <- function(x) {
  x <- x[is.finite(x)]
  m <- mean(x); s <- sd(x)
  if (!is.finite(s) || s == 0) return(0)
  mean((x - m)^3) / s^3
}

# Interaction allowed only when most cells have data to estimate it.
interaction_ok <- function(a, b, data) {
  tab <- table(data[[a]], data[[b]])
  length(tab) >= 4 && mean(tab >= 5) >= 0.75
}

back <- function(v, tr) if (tr == "log1p") expm1(v) else v

analyse <- function(outcome) {
  tr <- if (is.null(TRANSFORM[[outcome]])) "none" else TRANSFORM[[outcome]]
  d <- df
  raw <- suppressWarnings(as.numeric(d[[outcome]]))
  keep <- is.finite(raw) & raw >= 0
  d <- d[keep, , drop = FALSE]
  raw <- raw[keep]
  d$.y <- if (tr == "log1p") log1p(raw) else raw

  facs <- character(0)
  for (f in factors) {
    if (is.null(d[[f]])) next
    d[[f]] <- pool_levels(as.character(d[[f]]), min_n)
    if (nlevels(d[[f]]) >= 2) facs <- c(facs, f)
  }
  d <- d[complete.cases(d[, c(".y", facs), drop = FALSE]), , drop = FALSE]
  for (f in facs) d[[f]] <- droplevels(d[[f]])
  facs <- facs[vapply(facs, function(f) nlevels(d[[f]]) >= 2, logical(1))]
  if (length(facs) == 0 || nrow(d) < 30 || var(d$.y) == 0) {
    return(list(outcome = outcome, n = nrow(d), skipped = "datos insuficientes"))
  }

  terms <- facs
  for (pair in list(c("source", "device"), c("landing", "device"))) {
    if (all(pair %in% facs) && interaction_ok(pair[1], pair[2], d)) terms <- c(terms, paste(pair, collapse = ":"))
  }
  # Keep the model estimable: at least 10 observations per parameter.
  n_par <- function(ts) 1 + sum(vapply(ts, function(t) {
    prod(vapply(strsplit(t, ":", fixed = TRUE)[[1]], function(v) nlevels(d[[v]]) - 1, numeric(1)))
  }, numeric(1)))
  while (length(terms) > length(facs) && nrow(d) < 10 * n_par(terms)) terms <- terms[-length(terms)]

  a <- type2(".y", terms, d)
  fit <- a$fit
  sm <- summary(fit)

  effects <- lapply(a$effects, function(e) {
    e$significant <- e$p < alpha
    e
  })

  # Named empty lists, so jsonlite writes {} (not []) when nothing is filled.
  empty <- function() setNames(list(), character(0))
  levels_out <- empty(); posthoc <- empty(); bf <- empty(); kw <- empty()
  aov_fit <- aov(reformulate(terms, response = ".y"), data = d)
  for (f in facs) {
    g <- d[[f]]
    lv <- levels(g)
    levels_out[[f]] <- lapply(lv, function(l) {
      yy <- d$.y[g == l]
      list(level = l, n = length(yy),
           mean = back(mean(yy), tr),          # geometric-ish mean on log1p scale
           median = back(median(yy), tr))
    })
    bf[[f]] <- brown_forsythe(d$.y, g)
    kw[[f]] <- tryCatch(kruskal.test(d$.y, g)$p.value, error = function(e) NA)
    eff <- Filter(function(e) e$term == f, effects)
    if (length(eff) && eff[[1]]$significant) {
      th <- tryCatch(TukeyHSD(aov_fit, which = f)[[f]], error = function(e) NULL)
      if (!is.null(th)) {
        pairs <- strsplit(rownames(th), "-(?=[^-]*$)", perl = TRUE)
        rows <- lapply(seq_len(nrow(th)), function(i) {
          list(a = pairs[[i]][1], b = pairs[[i]][2], diff = th[i, "diff"],
               ratio = if (tr == "log1p") exp(th[i, "diff"]) else NA,
               lwr = th[i, "lwr"], upr = th[i, "upr"], p_adj = th[i, "p adj"])
        })
        posthoc[[f]] <- Filter(function(r) r$p_adj < alpha, rows)
      }
    }
  }

  # Interaction cells: the best and worst combination when the interaction is significant.
  cells <- list()
  for (e in effects) {
    if (!grepl(":", e$term, fixed = TRUE) || !e$significant) next
    vs <- strsplit(e$term, ":", fixed = TRUE)[[1]]
    key <- interaction(d[[vs[1]]], d[[vs[2]]], sep = " × ", drop = TRUE)
    agg <- aggregate(d$.y, list(cell = key), function(v) c(m = mean(v), n = length(v)))
    agg <- data.frame(cell = agg$cell, m = agg$x[, "m"], n = agg$x[, "n"])
    agg <- agg[agg$n >= 5, ]
    if (nrow(agg) < 2) next
    agg <- agg[order(agg$m), ]
    cells[[length(cells) + 1]] <- list(term = e$term,
      worst = list(cell = as.character(agg$cell[1]), n = agg$n[1], mean = back(agg$m[1], tr)),
      best = list(cell = as.character(agg$cell[nrow(agg)]), n = agg$n[nrow(agg)], mean = back(agg$m[nrow(agg)], tr)))
  }

  overall <- back(mean(d$.y), tr)
  list(
    outcome = outcome, transform = tr, n = nrow(d), terms = terms,
    overall_mean = overall,
    r2 = sm$r.squared, adj_r2 = sm$adj.r.squared,
    model_p = tryCatch({ fs <- sm$fstatistic; unname(pf(fs[1], fs[2], fs[3], lower.tail = FALSE)) }, error = function(e) NA),
    effects = effects, levels = levels_out, posthoc = posthoc, cells = cells,
    assumptions = list(brown_forsythe_p = bf, residual_skew = skewness(residuals(fit)),
                       homogeneous = all(unlist(bf) > 0.01, na.rm = TRUE)),
    robust = kw
  )
}

models <- lapply(outcomes, function(o) tryCatch(analyse(o), error = function(e) {
  warnings_out <<- c(warnings_out, paste(o, conditionMessage(e)))
  list(outcome = o, skipped = conditionMessage(e))
}))

cat(toJSON(list(analysis = input$analysis, n = nrow(df), alpha = alpha, models = models, warnings = warnings_out),
           auto_unbox = TRUE, digits = 6, na = "null", null = "null"))
