#!/usr/bin/env Rscript
# Rank-tracking statistics for the Web section's SEO suggestions.
#
# 1. Trend per keyword: least-squares slope of Google position over time
#    (positions per week; negative = climbing) with its p-value, so "it
#    dropped 3 places" is only reported when it's a trend and not day-to-day
#    noise of the SERP.
# 2. What on-page factors go with better positions *on this site*: a
#    factorial ANOVA (Type II) of log(position) on whether the keyword is in
#    the ranking page's title, in its h1, and the page's word-count band,
#    across all tracked keywords with a position. Effects come with partial
#    eta^2 and the geometric-mean position per level, plus a Kruskal-Wallis
#    cross-check. It's observational -- it tells the client "on your site,
#    pages with the keyword in the title rank ~2x better", not a law.
#
# Base R + jsonlite. stdin/stdout JSON:
#   in:  {"alpha": 0.05,
#         "series": [{"keyword": "...", "days": [0, 1, ...], "positions": [12, 11, ...]}],
#         "pages":  {"keyword": [...], "position": [...], "in_title": [...], "in_h1": [...], "words": [...]}}
#   out: {"trends": [...], "factors": {...} | null}

suppressPackageStartupMessages(library(jsonlite))

input <- fromJSON(file("stdin"), simplifyVector = TRUE, simplifyDataFrame = FALSE)
alpha <- if (is.null(input$alpha)) 0.05 else input$alpha

trends <- lapply(input$series, function(s) {
  x <- as.numeric(unlist(s$days)); y <- as.numeric(unlist(s$positions))
  ok <- is.finite(x) & is.finite(y)
  x <- x[ok]; y <- y[ok]
  if (length(y) < 5 || var(x) == 0) return(NULL)
  if (var(y) == 0) return(list(keyword = s$keyword, n = length(y), slope_per_week = 0, p = 1,
                               first = y[1], last = y[length(y)], significant = FALSE))
  fit <- lm(y ~ x)
  co <- summary(fit)$coefficients
  slope <- co["x", "Estimate"]
  p <- co["x", "Pr(>|t|)"]
  list(keyword = s$keyword, n = length(y), slope_per_week = slope * 7, p = p,
       first = y[1], last = y[length(y)],
       fitted_now = unname(predict(fit, data.frame(x = max(x)))),
       significant = is.finite(p) && p < alpha)
})
trends <- Filter(Negate(is.null), trends)

factors_out <- NULL
pg <- input$pages
if (!is.null(pg) && length(pg$position) >= 12) {
  d <- data.frame(
    position = as.numeric(unlist(pg$position)),
    in_title = factor(ifelse(unlist(pg$in_title), "sí", "no")),
    in_h1 = factor(ifelse(unlist(pg$in_h1), "sí", "no")),
    words = cut(as.numeric(unlist(pg$words)), c(-Inf, 299, 800, Inf), labels = c("<300", "300-800", ">800"))
  )
  d <- d[is.finite(d$position) & d$position > 0 & !is.na(d$words), ]
  d$y <- log(d$position)
  facs <- Filter(function(f) nlevels(droplevels(d[[f]])) >= 2 && min(table(droplevels(d[[f]]))) >= 3,
                 c("in_title", "in_h1", "words"))
  for (f in facs) d[[f]] <- droplevels(d[[f]])
  if (length(facs) && nrow(d) >= 12) {
    full <- lm(reformulate(facs, response = "y"), data = d)
    rss_full <- sum(residuals(full)^2); res_df <- df.residual(full)
    effects <- lapply(facs, function(t) {
      others <- setdiff(facs, t)
      m0 <- lm(if (length(others)) reformulate(others, response = "y") else y ~ 1, data = d)
      ss <- sum(residuals(m0)^2) - rss_full
      dft <- df.residual(m0) - res_df
      f <- (ss / dft) / (rss_full / res_df)
      p <- pf(f, dft, res_df, lower.tail = FALSE)
      lv <- lapply(levels(d[[t]]), function(l) list(level = l, n = sum(d[[t]] == l),
                                                    geo_mean_position = exp(mean(d$y[d[[t]] == l]))))
      list(term = t, df = dft, F = f, p = p, partial_eta2 = ss / (ss + rss_full),
           significant = p < alpha, levels = lv,
           kruskal_p = tryCatch(kruskal.test(d$y, d[[t]])$p.value, error = function(e) NA))
    })
    factors_out <- list(n = nrow(d), r2 = summary(full)$r.squared, effects = effects)
  }
}

cat(toJSON(list(trends = trends, factors = factors_out), auto_unbox = TRUE, digits = 6, na = "null", null = "null"))
