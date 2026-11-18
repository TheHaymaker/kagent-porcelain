// Who is signed in, from Identity-Aware Proxy (see IAM.md).
//
// IAP adds `x-goog-authenticated-user-email` ("accounts.google.com:alice@example.com")
// and a signed `x-goog-iap-jwt-assertion`. With IAP_AUDIENCE set, the JWT is
// verified and the plain header is ignored (recommended). LOCAL_DEV_USER stands
// in for IAP on local runs only (never when K_SERVICE is set, i.e. on Cloud Run).
import { OAuth2Client } from 'google-auth-library';

const oauth = new OAuth2Client();

export function parseEmailHeader(value) {
  if (!value) return undefined;
  const email = (value.includes(':') ? value.split(':').slice(1).join(':') : value).trim();
  return email.includes('@') ? email : undefined;
}

export async function verifyIapJwt(token, audience) {
  const { pubkeys } = await oauth.getIapPublicKeys();
  const ticket = await oauth.verifySignedJwtWithCertsAsync(token, pubkeys, audience, [
    'https://cloud.google.com/iap',
  ]);
  const email = ticket.getPayload()?.email;
  if (!email) throw new Error('IAP JWT has no email claim');
  return email;
}

export function identityMiddleware({ env = process.env, verify = verifyIapJwt } = {}) {
  return async (req, res, next) => {
    try {
      if (env.IAP_AUDIENCE) {
        const token = req.get('x-goog-iap-jwt-assertion');
        if (!token) return res.status(401).json({ error: 'Sign-in required' });
        req.user = { email: await verify(token, env.IAP_AUDIENCE) };
        return next();
      }
      const email = parseEmailHeader(req.get('x-goog-authenticated-user-email'));
      if (email) {
        req.user = { email };
        return next();
      }
      if (env.LOCAL_DEV_USER && !env.K_SERVICE) {
        req.user = { email: env.LOCAL_DEV_USER };
        return next();
      }
      return res.status(401).json({ error: 'Sign-in required' });
    } catch {
      return res.status(401).json({ error: 'Sign-in could not be verified' });
    }
  };
}
