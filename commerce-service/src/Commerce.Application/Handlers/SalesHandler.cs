using Commerce.Application.Shop;
using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class SalesHandler(IOrderRepository orders) : IRequestHandler
{
    public bool CanHandle(string operation) => operation == "sales.get";

    public async Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsVendor && !context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        var items = await orders.ListSalesAsync(context.Actor.Id, context.Actor.IsAdmin, cancellationToken);
        var result = items.Select(EntityMapper.ToDictionary).ToList();
        return ShopOutcome.Ok(new Dictionary<string, object?>
        {
            ["sales"] = result,
            ["gross_cents"] = result.Sum(x => (long)x["total_cents"]!),
            ["commission_cents"] = result.Sum(x => (long)x["commission_cents"]!),
            ["net_cents"] = result.Sum(x => (long)x["total_cents"]! - (long)x["commission_cents"]!)
        });
    }
}
