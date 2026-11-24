using System.Text.Json;
using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Application.Shop;

public sealed class ShopDispatcher(IEnumerable<IRequestHandler> handlers)
{
    private readonly IReadOnlyList<IRequestHandler> _handlers = handlers.ToList();

    public async Task<ShopOutcome> DispatchAsync(ShopCommand command, ActorContext actor, CancellationToken cancellationToken)
    {
        if (command.Id.Length > 128)
        {
            return ShopOutcome.Error(400, "INVALID_ID");
        }

        var raw = string.IsNullOrWhiteSpace(command.BodyJson) ? "{}" : command.BodyJson;
        if (raw.Length > 16_384)
        {
            return ShopOutcome.Error(400, "INVALID_JSON");
        }

        try
        {
            using var document = JsonDocument.Parse(raw);
            if (document.RootElement.ValueKind != JsonValueKind.Object)
            {
                return ShopOutcome.Error(400, "INVALID_JSON");
            }

            var handler = _handlers.FirstOrDefault(x => x.CanHandle(command.Operation));
            if (handler is null)
            {
                return ShopOutcome.Error(404, "UNKNOWN_OPERATION");
            }

            var context = new HandlerContext(command, actor, document.RootElement);
            return await handler.HandleAsync(context, cancellationToken);
        }
        catch (JsonException)
        {
            return ShopOutcome.Error(400, "INVALID_JSON");
        }
        catch (DomainException error)
        {
            return ShopOutcome.Error(error.Error.Status, error.Error.Code);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            throw;
        }
        catch
        {
            return ShopOutcome.Error(503, "UNAVAILABLE");
        }
    }
}
