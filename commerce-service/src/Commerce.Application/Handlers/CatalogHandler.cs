using Commerce.Application.Shop;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class CatalogHandler(IOfferRepository offers) : IRequestHandler
{
    public bool CanHandle(string operation) => operation == "catalog.list";

    public async Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        var city = context.Text("city");
        var category = context.Text("category");
        var result = await offers.ListPublishedAsync(city, category, cancellationToken);
        return ShopOutcome.Ok(new Dictionary<string, object?>
        {
            ["offers"] = result.Select(EntityMapper.ToDictionary).ToList()
        });
    }
}
