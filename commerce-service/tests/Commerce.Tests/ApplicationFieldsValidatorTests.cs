using System.Text.Json;
using Commerce.Application.Shop;
using Commerce.Application.Validation;
using Commerce.Domain;
using Commerce.Domain.Ports;
using Xunit;

namespace Commerce.Tests;

public sealed class ApplicationFieldsValidatorTests
{
    [Fact]
    public void Validate_WithCompleteFields_DoesNotThrow()
    {
        const string json = """
        {
          "organization":"ООО Ромашка",
          "title":"Выставка",
          "city":"Москва",
          "category":"Наука",
          "address":"ул. Ленина, 1",
          "contact_person":"Иван",
          "inn":"7700000000",
          "ogrn":"1027700000000",
          "bik":"044525000",
          "account":"40702810100000000001"
        }
        """;

        ApplicationFieldsValidator.Validate(Context(json));
    }

    [Fact]
    public void Validate_WithMissingRequiredDetails_ThrowsRequiredFields()
    {
        const string json = """
        {
          "organization":"ООО Ромашка",
          "title":"Выставка",
          "city":"Москва",
          "category":"Наука"
        }
        """;

        var error = Assert.Throws<DomainException>(() => ApplicationFieldsValidator.Validate(Context(json)));
        Assert.Equal(400, error.Error.Status);
        Assert.Equal("REQUIRED_FIELDS", error.Error.Code);
    }

    [Theory]
    [InlineData("770000000", "INVALID_INN")]
    [InlineData("77000a0000", "INVALID_INN")]
    public void Validate_WithBadInn_ThrowsBadRequest(string inn, string expectedCode)
    {
        var json = $$"""
        {
          "organization":"ООО Ромашка",
          "title":"Выставка",
          "city":"Москва",
          "category":"Наука",
          "address":"ул. Ленина, 1",
          "contact_person":"Иван",
          "inn":"{{inn}}",
          "ogrn":"1027700000000",
          "bik":"044525000",
          "account":"40702810100000000001"
        }
        """;

        var error = Assert.Throws<DomainException>(() => ApplicationFieldsValidator.Validate(Context(json)));

        Assert.Equal(400, error.Error.Status);
        Assert.Equal(expectedCode, error.Error.Code);
    }

    private static HandlerContext Context(string json)
    {
        using var document = JsonDocument.Parse(json);
        return new HandlerContext(
            new ShopCommand("applications.create", string.Empty, json, string.Empty),
            new ActorContext("vendor-1", "vendor"),
            document.RootElement.Clone());
    }
}
