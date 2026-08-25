/**
 * Decides whether the login page should skip its button and send the visitor
 * straight into the OIDC flow (the server's `oidc.autoLogin` option).
 */
export function shouldAutoLogin(
  config: { oidcEnabled: boolean; autoLogin: boolean } | undefined,
  params: URLSearchParams,
): boolean {
  if (!config?.oidcEnabled || !config.autoLogin) return false
  // A failed callback or a deliberate sign-out must render the page — an
  // unconditional redirect would loop the error away or undo the sign-out.
  return !params.get('error') && !params.get('signedOut')
}
