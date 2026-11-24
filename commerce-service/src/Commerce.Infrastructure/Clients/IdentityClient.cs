using System.Net;
using System.Text.Json;
using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Infrastructure.Clients;

public sealed class IdentityClient(HttpClient http, string baseUrl) : IActorResolver
{
    public async Task<ActorContext> ResolveAsync(string? authorization, CancellationToken cancellationToken)
    {
        if (string.IsNullOrWhiteSpace(authorization) || authorization.Length > 256 || !authorization.StartsWith("Bearer ", StringComparison.Ordinal))
        {
            throw new DomainException(new DomainError(401, "UNAUTHENTICATED"));
        }

        var url = baseUrl.TrimEnd('/');
        if (!url.EndsWith("/v1/session", StringComparison.Ordinal))
        {
            url += "/v1/session";
        }

        using var request = new HttpRequestMessage(HttpMethod.Get, url);
        request.Headers.TryAddWithoutValidation("Authorization", authorization);
        HttpResponseMessage response;
        try
        {
            response = await http.SendAsync(request, cancellationToken);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            throw new DomainException(DomainError.Timeout("MOCK_TIMEOUT"));
        }
        catch (HttpRequestException)
        {
            throw new DomainException(DomainError.Unavailable("IDENTITY_UNAVAILABLE"));
        }

        using (response)
        {
            if (response.StatusCode == HttpStatusCode.Unauthorized)
            {
                throw new DomainException(new DomainError(401, "UNAUTHENTICATED"));
            }

            if (response.StatusCode != HttpStatusCode.OK)
            {
                throw new DomainException(DomainError.Unavailable("IDENTITY_UNAVAILABLE"));
            }

            await using var stream = await response.Content.ReadAsStreamAsync(cancellationToken);
            using var document = await JsonDocument.ParseAsync(stream, cancellationToken: cancellationToken);
            var root = document.RootElement;
            var id = root.TryGetProperty("user", out var user) && user.TryGetProperty("id", out var idElement)
                ? idElement.GetString()
                : null;
            var role = user.TryGetProperty("role", out var roleElement) ? roleElement.GetString() : null;
            var expires = root.TryGetProperty("expires_at", out var expiresElement) && expiresElement.TryGetDateTimeOffset(out var value)
                ? value
                : DateTimeOffset.MinValue;

            if (string.IsNullOrEmpty(id) || expires <= DateTimeOffset.UtcNow || role is not ("customer" or "vendor" or "admin"))
            {
                throw new DomainException(new DomainError(401, "UNAUTHENTICATED"));
            }

            return new ActorContext(id, role);
        }
    }
}
